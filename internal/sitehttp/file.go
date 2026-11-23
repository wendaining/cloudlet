package sitehttp

import (
	"fmt"
	"io"
	"io/fs"
	"sync"
)

type readSeekCloser interface {
	io.ReadSeeker
	io.Closer
}

func asReadSeekCloser(site fs.FS, name string, file fs.File, size int64) readSeekCloser {
	if content, ok := file.(readSeekCloser); ok {
		return content
	}
	return &reopenedFile{site: site, name: name, file: file, size: size}
}

// reopenedFile adapts streaming files such as ZIP entries. Seek records the
// requested position without reading data so ServeContent can query the size
// and evaluate preconditions without reopening the file.
type reopenedFile struct {
	// ServeContent streams multipart ranges from a separate goroutine.
	mu     sync.Mutex
	closed bool
	site   fs.FS
	name   string
	file   fs.File
	size   int64
	cursor int64
	offset int64
}

func (f *reopenedFile) Seek(delta int64, whence int) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return 0, fs.ErrClosed
	}
	var base int64
	switch whence {
	case io.SeekStart:
	case io.SeekCurrent:
		base = f.cursor
	case io.SeekEnd:
		base = f.size
	default:
		return 0, fmt.Errorf("invalid seek origin %d: %w", whence, fs.ErrInvalid)
	}
	if next := base + delta; next >= 0 {
		f.cursor = next
		return next, nil
	}
	return 0, fmt.Errorf("invalid offset %d from %d: %w", delta, base, fs.ErrInvalid)
}

func (f *reopenedFile) Read(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return 0, fs.ErrClosed
	}
	if f.file == nil || f.cursor < f.offset {
		if err := f.closeCurrent(); err != nil {
			return 0, fmt.Errorf("close previous site file %q: %w", f.name, err)
		}
		file, err := f.site.Open(f.name)
		if err != nil {
			return 0, fmt.Errorf("reopen site file %q: %w", f.name, err)
		}
		f.file, f.offset = file, 0
	}
	if f.cursor > f.offset {
		skipped, err := io.CopyN(io.Discard, f.file, f.cursor-f.offset)
		f.offset += skipped
		if err != nil {
			return 0, err
		}
	}
	n, err := f.file.Read(p)
	f.offset += int64(n)
	f.cursor = f.offset
	return n, err
}

func (f *reopenedFile) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return f.closeCurrent()
}

func (f *reopenedFile) closeCurrent() error {
	if f.file == nil {
		return nil
	}
	file := f.file
	f.file = nil
	return file.Close()
}
