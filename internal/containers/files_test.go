package containers

import (
	"archive/tar"
	"bytes"
	"errors"
	"io"
	"io/fs"
	"testing"
)

func TestPreparedContainerFiles(t *testing.T) {
	files := []File{{Path: "worker/index.mjs", Data: []byte("export default {}")}}
	archive, err := archiveFiles(files)
	if err != nil {
		t.Fatal(err)
	}
	reader := tar.NewReader(bytes.NewReader(archive))
	header, err := reader.Next()
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(reader)
	if err != nil || header.Name != files[0].Path || header.Mode != 0o444 || !bytes.Equal(data, files[0].Data) {
		t.Fatalf("installed file: %+v %s %v", header, data, err)
	}
	if _, err := reader.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("archive tail: %v", err)
	}
	first, err := filesDigest(files)
	if err != nil {
		t.Fatal(err)
	}
	second, err := filesDigest(files)
	if err != nil || first != second {
		t.Fatal("identical files changed digest")
	}
	files[0].Data = []byte("changed")
	changed, err := filesDigest(files)
	if err != nil || changed == first {
		t.Fatal("changed files preserved digest")
	}
	for _, path := range []string{"../index.mjs", "/worker/index.mjs", "worker/../index.mjs", "worker\\index.mjs", ".", "worker:index.mjs"} {
		if _, err := archiveFiles([]File{{Path: path}}); !errors.Is(err, fs.ErrInvalid) {
			t.Fatalf("path %q: %v", path, err)
		}
	}
	if _, err := archiveFiles([]File{{Path: "worker/file"}, {Path: "worker/file"}}); !errors.Is(err, fs.ErrInvalid) {
		t.Fatalf("duplicate file: %v", err)
	}
}
