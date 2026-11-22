package siteartifact_test

import (
	"archive/zip"
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wendaining/cloudlet/internal/artifacts"
	"github.com/wendaining/cloudlet/internal/siteartifact"
)

type entry struct {
	name   string
	body   string
	mode   fs.FileMode
	method uint16
}

func TestExtractStaticSiteArtifact(t *testing.T) {
	files := []entry{
		{name: "index.html", body: "<h1>Cloudlet</h1>"},
		{name: "assets/app.js", body: "console.log('ready')", method: zip.Deflate},
		{name: "assets/", mode: fs.ModeDir | 0o755},
		{name: "nested/docs/style.css", body: "body { color: navy; }"},
	}
	var size int64
	for _, file := range files {
		size += int64(len(file.body))
	}
	store, digest := putArchive(t, makeZIP(t, files))
	parent := t.TempDir()
	dir, err := siteartifact.Extract(store, digest, parent, siteartifact.Limits{MaxEntries: len(files), MaxBytes: size})
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(dir) != parent {
		t.Fatalf("site directory %q is outside %q", dir, parent)
	}
	for _, file := range files {
		if file.mode.IsDir() {
			continue
		}
		got, err := os.ReadFile(filepath.Join(dir, file.name))
		if err != nil || string(got) != file.body {
			t.Fatalf("read %s = (%q, %v), want %q", file.name, got, err, file.body)
		}
	}
}

func TestExtractRejectsInvalidArchiveWithoutStagingSite(t *testing.T) {
	index := entry{name: "index.html", body: "ok"}
	for _, test := range []struct {
		name    string
		entries []entry
		limits  siteartifact.Limits
		want    string
	}{
		{"missing index", []entry{{name: "nested/index.html"}}, siteartifact.Limits{MaxEntries: 10, MaxBytes: 16}, "requires a regular index.html"},
		{"directory index", []entry{{name: "index.html/", mode: fs.ModeDir}}, siteartifact.Limits{MaxEntries: 10, MaxBytes: 16}, "requires a regular index.html"},
		{"duplicate", []entry{index, index}, siteartifact.Limits{MaxEntries: 10, MaxBytes: 16}, "duplicate archive entry"},
		{"ancestor file", []entry{index, {name: "a"}, {name: "a-b"}, {name: "a/b"}}, siteartifact.Limits{MaxEntries: 10, MaxBytes: 16}, "requires directory"},
		{"symlink", []entry{index, {name: "linked", body: "index.html", mode: fs.ModeSymlink}}, siteartifact.Limits{MaxEntries: 10, MaxBytes: 16}, "unsupported archive entry type"},
		{"named pipe", []entry{index, {name: "pipe", mode: fs.ModeNamedPipe}}, siteartifact.Limits{MaxEntries: 10, MaxBytes: 16}, "unsupported archive entry type"},
		{"expanded bytes", []entry{index, {name: "style.css", body: "123"}}, siteartifact.Limits{MaxEntries: 10, MaxBytes: 4}, "limit of 4 expanded bytes"},
		{"entry count", []entry{index, {name: "assets/", mode: fs.ModeDir}}, siteartifact.Limits{MaxEntries: 1, MaxBytes: 16}, "limit of 1 entries"},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, digest := putArchive(t, makeZIP(t, test.entries))
			assertFailure(t, store, digest, t.TempDir(), test.limits, test.want)
		})
	}
}

func TestExtractRejectsUnsafePaths(t *testing.T) {
	for _, name := range []string{"../escaped.html", "/escaped.html", "site/../index.html", "site//file", "C:/escaped.html", "site\\escaped.html", "site/./escaped.html"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("GODEBUG", "zipinsecurepath=1")
			parent := t.TempDir()
			destination := filepath.Join(parent, "sites")
			if err := os.Mkdir(destination, 0o700); err != nil {
				t.Fatal(err)
			}
			store, digest := putArchive(t, makeZIP(t, []entry{{name: "index.html"}, {name: name}}))
			assertFailure(t, store, digest, destination, siteartifact.Limits{MaxEntries: 2, MaxBytes: 16}, "invalid archive path")
			if _, err := os.Stat(filepath.Join(parent, "escaped.html")); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("escaped path exists or could not be checked: %v", err)
			}
		})
	}
}

func TestExtractCleansStagingAfterChecksumFailure(t *testing.T) {
	const body = "body that will be corrupted"
	data := makeZIP(t, []entry{{name: "index.html", body: "valid"}, {name: "assets/file.txt", body: body}})
	offset := bytes.Index(data, []byte(body))
	if offset < 0 {
		t.Fatal("ZIP fixture does not contain stored entry")
	}
	data[offset] ^= 1
	store, digest := putArchive(t, data)
	err := assertFailure(t, store, digest, t.TempDir(), siteartifact.Limits{MaxEntries: 2, MaxBytes: 128}, "assets/file.txt")
	if !errors.Is(err, zip.ErrChecksum) {
		t.Fatalf("error = %v, want ZIP checksum error", err)
	}
}

func TestExtractRejectsInvalidConfiguration(t *testing.T) {
	store, digest := putArchive(t, makeZIP(t, []entry{{name: "index.html"}}))
	err := assertFailure(t, store, digest, t.TempDir(), siteartifact.Limits{}, "positive limits required")
	if !errors.Is(err, fs.ErrInvalid) {
		t.Fatalf("error = %v, want fs.ErrInvalid", err)
	}
}

func makeZIP(t *testing.T, entries []entry) []byte {
	t.Helper()
	var dst bytes.Buffer
	writer := zip.NewWriter(&dst)
	for _, file := range entries {
		header := &zip.FileHeader{Name: file.name, Method: file.method}
		header.SetMode(file.mode | 0o600)
		output, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if !file.mode.IsDir() {
			if _, err := output.Write([]byte(file.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return dst.Bytes()
}

func putArchive(t *testing.T, data []byte) (*artifacts.Store, string) {
	t.Helper()
	store, err := artifacts.New(t.TempDir(), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	digest, err := store.Put(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	return store, digest
}

func assertFailure(t *testing.T, store *artifacts.Store, digest, directory string, limits siteartifact.Limits, want string) error {
	t.Helper()
	site, err := siteartifact.Extract(store, digest, directory, limits)
	if site != "" || err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("Extract = (%q, %v), want error containing %q", site, err, want)
	}
	entries, readErr := os.ReadDir(directory)
	if readErr != nil || len(entries) != 0 {
		t.Fatalf("expected empty destination, got %v, %v", entries, readErr)
	}
	return err
}
