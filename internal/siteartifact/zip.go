// Package siteartifact prepares validated static-site directories from artifacts.
package siteartifact

import (
	"archive/zip"
	"compress/flate"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path"
	"slices"
	"strings"

	"github.com/wendaining/cloudlet/internal/artifacts"
)

// ErrInvalidArchive identifies ZIP content that cannot be published as a site.
var ErrInvalidArchive = errors.New("invalid static site archive")

const maxPathComponentBytes = 255

// Limits bounds the number of ZIP entries, including explicit directories,
// and the total uncompressed bytes across all regular files.
type Limits struct {
	MaxEntries int
	MaxBytes   int64
}

// Extract opens an existing ZIP artifact and extracts it into a new private
// directory under parent. The caller owns the returned directory on success.
func Extract(store *artifacts.Store, digest, parent string, limits Limits) (directory string, err error) {
	if store == nil || parent == "" || limits.MaxEntries <= 0 || limits.MaxBytes <= 0 {
		return "", fmt.Errorf("configure site extraction: store, parent directory and positive limits required: %w", fs.ErrInvalid)
	}
	artifact, err := store.Open(digest)
	if err != nil {
		return "", fmt.Errorf("open site archive %q: %w", digest, err)
	}
	var staging string
	defer func() {
		if closeErr := artifact.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close site archive %s: %w", digest, closeErr))
		}
		if err != nil && staging != "" {
			directory = ""
			if removeErr := os.RemoveAll(staging); removeErr != nil {
				err = errors.Join(err, fmt.Errorf("remove incomplete site directory %q: %w", staging, removeErr))
			}
		}
	}()
	info, err := artifact.Stat()
	if err != nil {
		return "", fmt.Errorf("inspect site archive %s: %w", digest, err)
	}
	if err := checkDirectoryLimit(artifact, info.Size(), limits.MaxEntries); err != nil {
		return "", fmt.Errorf("inspect site ZIP directory %s: %w", digest, classifyZIPError(err))
	}
	archive, err := zip.NewReader(artifact, info.Size())
	if err != nil {
		return "", fmt.Errorf("read site archive %s: %w", digest, classifyZIPError(err))
	}
	if err := validate(archive, limits); err != nil {
		return "", fmt.Errorf("validate site archive %s: %w: %w", digest, ErrInvalidArchive, err)
	}

	staging, err = os.MkdirTemp(parent, ".site-")
	if err != nil {
		return "", fmt.Errorf("create site directory under %q: %w", parent, err)
	}
	if err := unpack(staging, archive.File); err != nil {
		return "", fmt.Errorf("expand site archive %s: %w", digest, classifyZIPError(err))
	}
	if err := syncDirectory(parent); err != nil {
		return "", fmt.Errorf("persist site directory under %q: %w", parent, err)
	}
	return staging, nil
}

func classifyZIPError(err error) error {
	_, corrupt := errors.AsType[flate.CorruptInputError](err)
	if errors.Is(err, zip.ErrFormat) || errors.Is(err, zip.ErrInsecurePath) || errors.Is(err, zip.ErrAlgorithm) ||
		errors.Is(err, zip.ErrChecksum) || errors.Is(err, io.ErrUnexpectedEOF) || corrupt {
		return fmt.Errorf("%w: %w", ErrInvalidArchive, err)
	}
	return err
}

func validate(archive *zip.Reader, limits Limits) error {
	if len(archive.File) > limits.MaxEntries {
		return fmt.Errorf("archive exceeds the limit of %d entries", limits.MaxEntries)
	}
	entries := make(map[string]bool, len(archive.File))
	remaining := uint64(limits.MaxBytes)
	for _, file := range archive.File {
		directory := file.Mode().Type() == fs.ModeDir
		name := entryName(file)
		if name == "." || !fs.ValidPath(name) || strings.ContainsAny(name, "\x00\\:") {
			return fmt.Errorf("invalid archive path %q", file.Name)
		}
		for component := range strings.SplitSeq(name, "/") {
			if len(component) > maxPathComponentBytes {
				return fmt.Errorf("archive path component exceeds %d bytes in %q", maxPathComponentBytes, name)
			}
		}
		if _, exists := entries[name]; exists {
			return fmt.Errorf("duplicate archive entry %q", name)
		}
		if !(directory || file.Mode().IsRegular()) {
			return fmt.Errorf("unsupported archive entry type for %q: %v", name, file.Mode())
		}
		if directory && file.UncompressedSize64 != 0 {
			return fmt.Errorf("archive directory %q contains data", name)
		}
		if file.UncompressedSize64 > remaining {
			return fmt.Errorf("archive exceeds the limit of %d expanded bytes at %q", limits.MaxBytes, name)
		}
		remaining -= file.UncompressedSize64
		entries[name] = directory
	}

	if directory, exists := entries["index.html"]; !exists || directory {
		return errors.New("archive requires a regular index.html at the site root")
	}
	names := slices.Sorted(maps.Keys(entries))
	for _, name := range names {
		if entries[name] {
			continue
		}
		prefix := name + "/"
		child, _ := slices.BinarySearch(names, prefix)
		if child < len(names) && strings.HasPrefix(names[child], prefix) {
			return fmt.Errorf("archive path %q requires directory %q, which is a file", names[child], name)
		}
	}
	return nil
}

func entryName(file *zip.File) string {
	if file.Mode().IsDir() {
		return strings.TrimSuffix(file.Name, "/")
	}
	return file.Name
}

func unpack(staging string, files []*zip.File) (err error) {
	root, err := os.OpenRoot(staging)
	if err != nil {
		return fmt.Errorf("open site directory %q: %w", staging, err)
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close site directory %q: %w", staging, closeErr))
		}
	}()
	for _, file := range files {
		if err := extractEntry(root, file); err != nil {
			return err
		}
	}
	return syncTree(root)
}

func extractEntry(root *os.Root, file *zip.File) (err error) {
	name := entryName(file)
	if file.Mode().IsDir() {
		if err := root.MkdirAll(name, 0o700); err != nil {
			return fmt.Errorf("create site directory %q: %w", name, err)
		}
		return nil
	}
	if err := root.MkdirAll(path.Dir(name), 0o700); err != nil {
		return fmt.Errorf("create parent directory for %q: %w", name, err)
	}
	input, err := file.Open()
	if err != nil {
		return fmt.Errorf("open ZIP entry %q: %w", name, err)
	}
	defer func() {
		if closeErr := input.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close ZIP entry %q: %w", name, closeErr))
		}
	}()
	output, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create site file %q: %w", name, err)
	}
	defer func() {
		if closeErr := output.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close site file %q: %w", name, closeErr))
		}
	}()
	// archive/zip verifies the declared size and checksum while reading.
	if _, err := io.Copy(output, input); err != nil {
		return fmt.Errorf("extract ZIP entry %q: %w", name, err)
	}
	if err := output.Sync(); err != nil {
		return fmt.Errorf("sync site file %q: %w", name, err)
	}
	return nil
}

func syncTree(root *os.Root) error {
	return fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("walk site directory %q: %w", name, err)
		}
		if !entry.IsDir() {
			return nil
		}
		directory, err := root.Open(name)
		if err != nil {
			return fmt.Errorf("open site directory %q: %w", name, err)
		}
		return closeAfterSync(directory, name)
	})
}

func syncDirectory(name string) error {
	directory, err := os.Open(name)
	if err != nil {
		return fmt.Errorf("open directory %q: %w", name, err)
	}
	return closeAfterSync(directory, name)
}

func closeAfterSync(directory *os.File, name string) (err error) {
	defer func() {
		if closeErr := directory.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close directory %q: %w", name, closeErr))
		}
	}()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("sync directory %q: %w", name, err)
	}
	return nil
}
