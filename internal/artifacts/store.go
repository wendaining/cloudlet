// Package artifacts stores immutable files by their SHA-256 digest.
package artifacts

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
)

var (
	ErrTooLarge      = errors.New("artifact exceeds size limit")
	ErrInvalidDigest = errors.New("invalid artifact digest")
)

// Store uses a dedicated directory owned by the platform. Its methods can be
// called concurrently. The caller must close it after all operations finish.
type Store struct {
	root     *os.Root
	maxBytes int64
}

// New opens an existing artifact directory and sets a positive per-file limit.
func New(directory string, maxBytes int64) (*Store, error) {
	if directory == "" || maxBytes <= 0 {
		return nil, fmt.Errorf("configure artifact store: directory and positive size limit required: %w", fs.ErrInvalid)
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, fmt.Errorf("open artifact directory %q: %w", directory, err)
	}
	return &Store{root: root, maxBytes: maxBytes}, nil
}

// Close releases the store's directory handle.
func (s *Store) Close() error {
	return s.root.Close()
}

// Put stores the input and returns its lowercase SHA-256 digest. If directory
// sync or temporary file cleanup fails, retrying can reuse the complete artifact.
func (s *Store) Put(input io.Reader) (digest string, err error) {
	if input == nil {
		return "", fmt.Errorf("write artifact: input is required: %w", fs.ErrInvalid)
	}
	name := ".upload-" + rand.Text()
	file, err := s.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", fmt.Errorf("create temporary artifact in %q: %w", s.root.Name(), err)
	}
	defer func() {
		if removeErr := s.root.Remove(name); removeErr != nil {
			digest = ""
			err = errors.Join(err, fmt.Errorf("remove temporary artifact %q: %w", name, removeErr))
		}
	}()

	digest, err = s.write(file, input)
	if err != nil {
		return "", err
	}
	// A hard link publishes the complete file without replacing a digest
	// that another writer has already published.
	if err := s.root.Link(name, digest); err != nil {
		if !errors.Is(err, fs.ErrExist) {
			return "", fmt.Errorf("publish artifact %s: %w", digest, err)
		}
		if err := s.checkRegular(digest); err != nil {
			return "", fmt.Errorf("reuse artifact %s: %w", digest, err)
		}
	}
	// Duplicate writers also sync the directory because the winner might
	// have published the link without completing its own sync yet.
	if err := s.syncDirectory(); err != nil {
		return "", fmt.Errorf("persist published artifact %s: %w", digest, err)
	}
	return digest, nil
}

func (s *Store) write(file *os.File, input io.Reader) (digest string, err error) {
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close temporary artifact %q: %w", file.Name(), closeErr))
		}
	}()
	hash := sha256.New()
	size, err := io.Copy(io.MultiWriter(file, hash), io.LimitReader(input, s.maxBytes))
	if err != nil {
		return "", fmt.Errorf("copy artifact to temporary file %q: %w", file.Name(), err)
	}
	if size == s.maxBytes {
		var extra [1]byte
		_, err := io.ReadFull(input, extra[:])
		switch {
		case err == nil:
			return "", fmt.Errorf("write artifact with limit %d bytes: %w", s.maxBytes, ErrTooLarge)
		case err != io.EOF:
			return "", fmt.Errorf("check end of artifact input: %w", err)
		}
	}
	if err := file.Sync(); err != nil {
		return "", fmt.Errorf("sync temporary artifact %q: %w", file.Name(), err)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// Open returns a read-only file for a canonical digest. The caller closes it.
func (s *Store) Open(digest string) (*os.File, error) {
	if len(digest) != sha256.Size*2 || strings.Trim(digest, "0123456789abcdef") != "" {
		return nil, fmt.Errorf("open artifact %q: %w", digest, ErrInvalidDigest)
	}
	if err := s.checkRegular(digest); err != nil {
		return nil, err
	}
	file, err := s.root.Open(digest)
	if err != nil {
		return nil, fmt.Errorf("open artifact %s: %w", digest, err)
	}
	return file, nil
}

func (s *Store) checkRegular(digest string) error {
	info, err := s.root.Lstat(digest)
	if err != nil {
		return fmt.Errorf("inspect artifact %s: %w", digest, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("artifact %s is not a regular file: %w", digest, fs.ErrInvalid)
	}
	return nil
}

func (s *Store) syncDirectory() error {
	directory, err := s.root.Open(".")
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}
