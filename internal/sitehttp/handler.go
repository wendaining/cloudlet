// Package sitehttp serves files from a validated, read-only static site.
package sitehttp

import (
	"bufio"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"
)

// New returns a handler for the provided site filesystem. The caller keeps
// the filesystem readable for the lifetime of the handler.
func New(site fs.FS) (http.Handler, error) {
	if site == nil {
		return nil, fmt.Errorf("configure static site handler: filesystem is required: %w", fs.ErrInvalid)
	}
	return handler{site: site}, nil
}

type handler struct {
	site fs.FS
}

func (h handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	name, ok := strings.CutPrefix(r.URL.Path, "/")
	if !ok || strings.ContainsAny(name, "\x00\\:") {
		http.NotFound(w, r)
		return
	}
	directoryURL := strings.HasSuffix(r.URL.Path, "/")
	name = strings.TrimSuffix(name, "/")
	if r.URL.Path == "/" {
		name = "."
	} else if name == "." || !fs.ValidPath(name) {
		http.NotFound(w, r)
		return
	}

	info, err := fs.Stat(h.site, name)
	if err != nil {
		serveFileError(w, r, name, err)
		return
	}
	if info.IsDir() {
		name = path.Join(name, "index.html")
		info, err = fs.Stat(h.site, name)
		if err != nil {
			serveFileError(w, r, name, err)
			return
		}
		if info.Mode().IsRegular() && !directoryURL {
			target := r.URL.EscapedPath() + "/"
			if r.URL.RawQuery != "" {
				target += "?" + r.URL.RawQuery
			}
			http.Redirect(w, r, target, http.StatusMovedPermanently)
			return
		}
	} else if directoryURL {
		http.NotFound(w, r)
		return
	}
	if !info.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}

	file, err := h.site.Open(name)
	if err != nil {
		serveFileError(w, r, name, err)
		return
	}
	defer closeFile(r, name, file)
	body := bufio.NewReader(file)
	contentType := mime.TypeByExtension(path.Ext(name))
	if contentType == "" {
		sample, err := body.Peek(512)
		if err != nil && !errors.Is(err, io.EOF) {
			serveFileError(w, r, name, err)
			return
		}
		contentType = http.DetectContentType(sample)
	}
	hash := sha256.New()
	length, err := io.Copy(hash, body)
	if err == nil && length != info.Size() {
		err = fmt.Errorf("read %d of %d bytes: %w", length, info.Size(), io.ErrUnexpectedEOF)
	}
	if err != nil {
		serveFileError(w, r, name, err)
		return
	}
	etag := fmt.Sprintf(`"%x"`, hash.Sum(nil))
	lastModified := info.ModTime().UTC().Truncate(time.Second)
	headers := w.Header()
	headers.Set("ETag", etag)
	headers.Set("Cache-Control", cachePolicy(contentType))
	if !lastModified.IsZero() {
		headers.Set("Last-Modified", lastModified.Format(http.TimeFormat))
	}
	if notModified(r.Header, etag, lastModified) {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	headers.Set("Content-Type", contentType)
	headers.Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	if r.Method == http.MethodHead {
		return
	}
	if seeker, ok := file.(io.Seeker); ok {
		if _, err := seeker.Seek(0, io.SeekStart); err != nil {
			serveFileError(w, r, name, err)
			return
		}
	} else {
		reopened, err := h.site.Open(name)
		if err != nil {
			serveFileError(w, r, name, err)
			return
		}
		defer closeFile(r, name, reopened)
		file = reopened
	}
	written, err := io.Copy(w, file)
	if err == nil && written != info.Size() {
		err = fmt.Errorf("sent %d of %d bytes: %w", written, info.Size(), io.ErrUnexpectedEOF)
	}
	if err != nil {
		slog.ErrorContext(r.Context(), "send static site file", "path", name, "error", err)
		panic(http.ErrAbortHandler)
	}
}

func closeFile(r *http.Request, name string, file fs.File) {
	if err := file.Close(); err != nil {
		slog.ErrorContext(r.Context(), "close static site file", "path", name, "error", err)
	}
}

func serveFileError(w http.ResponseWriter, r *http.Request, name string, err error) {
	w.Header().Del("ETag")
	w.Header().Del("Last-Modified")
	w.Header().Del("Cache-Control")
	if errors.Is(err, fs.ErrNotExist) {
		http.NotFound(w, r)
		return
	}
	slog.ErrorContext(r.Context(), "read static site file", "path", name, "error", err)
	http.Error(w, "could not read site file", http.StatusInternalServerError)
}
