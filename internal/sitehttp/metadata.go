package sitehttp

import (
	"bufio"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"sync"
	"time"
)

type fileMetadata struct {
	contentType string
	etag        string
}

type fileVersion struct {
	size     int64
	modified time.Time
	changed  fileChange
}

type fileChange struct {
	device, inode uint64
	sec, nsec     int64
}

type metadataEntry struct {
	mu       sync.Mutex
	version  fileVersion
	metadata fileMetadata
}

func (h *handler) fileMetadata(name string, file fs.File, info fs.FileInfo, content io.ReadSeeker) (fileMetadata, error) {
	change, trackable := fileChangeTime(info)
	if !trackable && !h.immutable {
		return readFileMetadata(name, content, info.Size())
	}
	version := fileVersion{size: info.Size(), modified: info.ModTime(), changed: change}
	value, _ := h.metadata.LoadOrStore(name, &metadataEntry{})
	entry := value.(*metadataEntry)
	entry.mu.Lock()
	defer entry.mu.Unlock()
	if entry.metadata.etag != "" && entry.version == version {
		return entry.metadata, nil
	}
	metadata, err := readFileMetadata(name, content, info.Size())
	if err != nil {
		return fileMetadata{}, err
	}
	after, err := file.Stat()
	if err != nil {
		return fileMetadata{}, fmt.Errorf("check file metadata after reading: %w", err)
	}
	afterChange, _ := fileChangeTime(after)
	if (fileVersion{size: after.Size(), modified: after.ModTime(), changed: afterChange}) != version {
		return fileMetadata{}, errors.New("site file changed while computing its validator")
	}
	entry.version, entry.metadata = version, metadata
	return metadata, nil
}

func readFileMetadata(name string, content io.ReadSeeker, size int64) (fileMetadata, error) {
	body := bufio.NewReader(content)
	contentType := mime.TypeByExtension(path.Ext(name))
	if contentType == "" {
		sample, err := body.Peek(512)
		if err != nil && !errors.Is(err, io.EOF) {
			return fileMetadata{}, err
		}
		contentType = http.DetectContentType(sample)
	}
	hash := sha256.New()
	length, err := io.Copy(hash, body)
	if err == nil && length != size {
		err = fmt.Errorf("read %d of %d bytes: %w", length, size, io.ErrUnexpectedEOF)
	}
	if err != nil {
		return fileMetadata{}, err
	}
	if _, err := content.Seek(0, io.SeekStart); err != nil {
		return fileMetadata{}, err
	}
	return fileMetadata{contentType: contentType, etag: fmt.Sprintf(`"%x"`, hash.Sum(nil))}, nil
}
