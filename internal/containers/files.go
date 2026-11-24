package containers

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"strings"

	"github.com/moby/moby/client"
)

// File is a platform-prepared regular file copied before a container starts.
// Path is relative to the container root. Files are installed read-only.
type File struct {
	Path string
	Data []byte
}

func archiveFiles(files []File) ([]byte, error) {
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	seen := make(map[string]bool, len(files))
	for _, file := range files {
		if !fs.ValidPath(file.Path) || file.Path == "." || strings.ContainsAny(file.Path, "\\:\x00") || seen[file.Path] || len(file.Data) > 32<<20 {
			return nil, fmt.Errorf("invalid container file path or size %q: %w", file.Path, fs.ErrInvalid)
		}
		seen[file.Path] = true
		if err := writer.WriteHeader(&tar.Header{Name: file.Path, Mode: 0o444, Size: int64(len(file.Data))}); err != nil {
			return nil, fmt.Errorf("archive container file %q: %w", file.Path, err)
		}
		if _, err := writer.Write(file.Data); err != nil {
			return nil, err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func filesDigest(files []File) (string, error) {
	if len(files) == 0 {
		return "", nil
	}
	archive, err := archiveFiles(files)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(archive)), nil
}

// FilesDigest identifies the exact ordered file archive installed by Create.
func (s Spec) FilesDigest() (string, error) { return filesDigest(s.Files) }

func (a *Adapter) installFiles(ctx context.Context, created Container, spec Spec) (err error) {
	archive, err := archiveFiles(spec.Files)
	if err != nil {
		return err
	}
	if _, err := a.docker.CopyToContainer(ctx, created.ID, client.CopyToContainerOptions{DestinationPath: "/", Content: bytes.NewReader(archive)}); err != nil {
		return fmt.Errorf("install files in container %s: %w", created.ID, err)
	}
	return nil
}
