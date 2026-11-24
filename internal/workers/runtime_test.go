package workers_test

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/wendaining/cloudlet/internal/durableobjects"
	"github.com/wendaining/cloudlet/internal/workers"
)

const compatibilityDate = "2026-10-12"

func TestPrepare(t *testing.T) {
	entry := "dist/hello-world.mjs"
	module := []byte("export default {fetch() { return new Response('你好') }};\n")
	bundle := writeBundle(t, entry, module)

	prepared, err := workers.Prepare(bundle, entry, compatibilityDate)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(prepared.Module, module) {
		t.Fatalf("module = %q, want %q", prepared.Module, module)
	}
	if prepared.Entry != entry || prepared.CompatibilityDate != compatibilityDate {
		t.Fatalf("prepared metadata = entry %q, compatibility date %q", prepared.Entry, prepared.CompatibilityDate)
	}
	for _, expected := range []string{
		`name = "dist/hello-world.mjs", esModule = embed "worker.mjs"`,
		`compatibilityDate = "2026-10-12"`,
		`address = "*:8080", http = (), service = "worker"`,
	} {
		if !bytes.Contains(prepared.Config, []byte(expected)) {
			t.Errorf("config does not contain %q:\n%s", expected, prepared.Config)
		}
	}
	if bytes.Contains(prepared.Config, []byte("durableObjectStorage")) {
		t.Fatal("stateless config enables Durable Object storage")
	}
	if args := workers.ServeArgs(); !slices.Equal(args, []string{"serve", "/worker/config.capnp"}) {
		t.Errorf("serve arguments = %q", args)
	}
}

func TestPrepareWithDurableObjectsRejectsInvalidBinding(t *testing.T) {
	entry := "worker.mjs"
	bundle := writeBundle(t, entry, []byte("export default {fetch() { return new Response() }}"))
	namespaces := []durableobjects.Namespace{{ID: uuid.New(), ClassName: "Counter", Bindings: []string{`BAD"BINDING`}}}
	prepared, err := workers.PrepareWithDurableObjects(bundle, entry, compatibilityDate, namespaces)
	if err == nil || !strings.Contains(err.Error(), "must be an ASCII JavaScript identifier") {
		t.Fatalf("PrepareWithDurableObjects error = %v, want binding diagnostic", err)
	}
	if prepared.Config != nil || prepared.Module != nil {
		t.Fatal("PrepareWithDurableObjects returned files after a validation error")
	}
}

func TestPrepareRejectsInvalidInput(t *testing.T) {
	bundle := writeBundle(t, "dist/index.js", []byte("export default {fetch() { return new Response() }};"))
	if err := os.Mkdir(filepath.Join(bundle, "folder.js"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bundle, "empty.js"), []byte(" \n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bundle, "invalid.js"), []byte{0xff}, 0600); err != nil {
		t.Fatal(err)
	}

	for _, tt := range []struct {
		name, entry, date, diagnostic string
	}{
		{"empty entry", "", compatibilityDate, `entry ""`},
		{"absolute entry", "/index.js", compatibilityDate, `/index.js`},
		{"parent path", "dist/../index.js", compatibilityDate, `dist/../index.js`},
		{"Windows path", `dist\index.js`, compatibilityDate, `must be a relative`},
		{"quoted path", `dist/"index.js`, compatibilityDate, `must be a relative`},
		{"non JavaScript entry", "index.ts", compatibilityDate, `index.ts`},
		{"missing entry", "missing.js", compatibilityDate, `inspect entry "missing.js"`},
		{"directory entry", "folder.js", compatibilityDate, `must be a regular file`},
		{"empty source", "empty.js", compatibilityDate, `must contain non-empty UTF-8`},
		{"invalid source encoding", "invalid.js", compatibilityDate, `must contain non-empty UTF-8`},
		{"missing date", "dist/index.js", "", `compatibility date ""`},
		{"invalid date format", "dist/index.js", "2026-1-01", `YYYY-MM-DD`},
		{"invalid calendar date", "dist/index.js", "2026-02-29", `compatibility date "2026-02-29"`},
		{"date below runtime range", "dist/index.js", "1999-12-31", `outside the range`},
		{"date beyond runtime range", "dist/index.js", "2026-10-20", `outside the range`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			prepared, err := workers.Prepare(bundle, tt.entry, tt.date)
			if err == nil || !strings.Contains(err.Error(), tt.diagnostic) {
				t.Fatalf("Prepare error = %v, want %q", err, tt.diagnostic)
			}
			if prepared.Config != nil || prepared.Module != nil {
				t.Fatal("Prepare returned files after an input error")
			}
			if tt.entry == "missing.js" && !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("missing entry error lost its cause: %v", err)
			}
		})
	}
}

func TestPrepareRejectsEntryOutsideBundle(t *testing.T) {
	outside := writeBundle(t, "outside.js", []byte("export default {}"))
	bundle := t.TempDir()
	if err := os.Symlink(filepath.Join("..", filepath.Base(outside), "outside.js"), filepath.Join(bundle, "index.js")); err != nil {
		t.Fatal(err)
	}
	if _, err := workers.Prepare(bundle, "index.js", compatibilityDate); err == nil || !strings.Contains(err.Error(), `inspect entry "index.js"`) {
		t.Fatalf("Prepare error = %v, want entry diagnostic", err)
	}
}

func writeBundle(t *testing.T, entry string, module []byte) string {
	t.Helper()
	dir := t.TempDir()
	file := filepath.Join(dir, filepath.FromSlash(entry))
	if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, module, 0600); err != nil {
		t.Fatal(err)
	}
	return dir
}
