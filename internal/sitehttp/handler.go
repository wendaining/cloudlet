// Package sitehttp serves files from a validated, read-only static site.
package sitehttp

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"path"
	"strings"
	"sync"
)

// New returns a handler for the provided site filesystem. The caller keeps
// the filesystem readable for the lifetime of the handler.
func New(site fs.FS) (http.Handler, error) {
	return NewWithOptions(site, Options{})
}

// Options configures the behavior of an individual static site.
type Options struct {
	SPAFallback bool
	// Immutable permits reusing file digests for filesystems without file
	// change metadata. The caller must keep the files unchanged.
	Immutable bool
}

// NewWithOptions returns a handler with the given site options.
func NewWithOptions(site fs.FS, options Options) (http.Handler, error) {
	if site == nil {
		return nil, fmt.Errorf("configure static site handler: filesystem is required: %w", fs.ErrInvalid)
	}
	return &handler{site: site, spaFallback: options.SPAFallback, immutable: options.Immutable}, nil
}

type handler struct {
	site        fs.FS
	spaFallback bool
	immutable   bool
	metadata    sync.Map
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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
	if h.spaFallback && errors.Is(err, fs.ErrNotExist) && path.Ext(name) == "" {
		w.Header().Add("Vary", "Accept, Sec-Fetch-Mode, Sec-Fetch-Dest")
		if isHTMLNavigation(r.Header) {
			h.serveFile(w, r, "index.html")
			return
		}
	}
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
	h.writeFile(w, r, name)
}

func (h *handler) serveFile(w http.ResponseWriter, r *http.Request, name string) {
	info, err := fs.Stat(h.site, name)
	if err != nil {
		serveFileError(w, r, name, err)
		return
	}
	if !info.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	h.writeFile(w, r, name)
}

func (h *handler) writeFile(w http.ResponseWriter, r *http.Request, name string) {
	file, err := h.site.Open(name)
	if err != nil {
		serveFileError(w, r, name, err)
		return
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		defer closeFile(r, name, file)
		if err == nil {
			err = fs.ErrNotExist
		}
		serveFileError(w, r, name, err)
		return
	}
	content := asReadSeekCloser(h.site, name, file, info.Size())
	defer closeFile(r, name, content)
	metadata, err := h.fileMetadata(name, file, info, content)
	if err != nil {
		serveFileError(w, r, name, err)
		return
	}
	headers := w.Header()
	headers.Set("Content-Type", metadata.contentType)
	headers.Set("ETag", metadata.etag)
	headers.Set("Cache-Control", cachePolicy(metadata.contentType))
	http.ServeContent(fileResponse{w}, combineETagHeaders(r), name, info.ModTime(), content)
}

func closeFile(r *http.Request, name string, file io.Closer) {
	if err := file.Close(); err != nil {
		slog.ErrorContext(r.Context(), "close static site file", "path", name, "error", err)
	}
}

func serveFileError(w http.ResponseWriter, r *http.Request, name string, err error) {
	if errors.Is(err, fs.ErrNotExist) {
		http.NotFound(w, r)
		return
	}
	slog.ErrorContext(r.Context(), "read static site file", "path", name, "error", err)
	http.Error(w, "could not read site file", http.StatusInternalServerError)
}
