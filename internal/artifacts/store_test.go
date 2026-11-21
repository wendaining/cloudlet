package artifacts_test

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wendaining/cloudlet/internal/artifacts"
)

func TestArtifactRoundTrip(t *testing.T) {
	const content = "exactly at the limit\x00"
	store, directory := newStore(t, int64(len(content)))
	for _, input := range []string{content, ""} {
		digest := put(t, store, input)
		if digest != digestOf(input) {
			t.Fatalf("digest = %s, want %s", digest, digestOf(input))
		}
		assertContent(t, store, digest, input)
	}
	assertFiles(t, directory, 2)
}

func TestConcurrentWritesReuseArtifact(t *testing.T) {
	const content = "shared artifact"
	store, directory := newStore(t, 64)
	other, err := artifacts.New(directory, 64)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeFile(t, other) })
	t.Run("writers", func(t *testing.T) {
		for index := range 16 {
			t.Run(fmt.Sprint(index), func(t *testing.T) {
				t.Parallel()
				writer := store
				if index%2 == 0 {
					writer = other
				}
				digest := put(t, writer, content)
				if digest != digestOf(content) {
					t.Fatalf("digest = %s, want %s", digest, digestOf(content))
				}
				assertContent(t, writer, digest, content)
			})
		}
	})
	assertFiles(t, directory, 1)
}

func TestArtifactIsReadableOnlyAfterInputEnds(t *testing.T) {
	const content = "still uploading"
	store, directory := newStore(t, 64)
	reader, writer := io.Pipe()
	defer closeFile(t, reader)
	defer closeFile(t, writer)
	result := make(chan error, 1)
	go func() {
		digest, err := store.Put(reader)
		if err == nil && digest != digestOf(content) {
			err = fmt.Errorf("digest = %s, want %s", digest, digestOf(content))
		}
		result <- err
	}()
	if _, err := io.WriteString(writer, content); err != nil {
		t.Fatal(err)
	}
	assertUnavailable(t, store, digestOf(content), fs.ErrNotExist)
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	assertContent(t, store, digestOf(content), content)
	assertFiles(t, directory, 1)
}

func TestOversizedArtifactLeavesNoFiles(t *testing.T) {
	store, directory := newStore(t, 3)
	if digest, err := store.Put(strings.NewReader("abcd")); digest != "" || !errors.Is(err, artifacts.ErrTooLarge) {
		t.Fatalf("Put = (%q, %v), want ErrTooLarge", digest, err)
	}
	assertUnavailable(t, store, digestOf("abc"), fs.ErrNotExist)
	assertUnavailable(t, store, digestOf("abcd"), fs.ErrNotExist)
	assertFiles(t, directory, 0)
}

func TestInterruptedInputLeavesNoFiles(t *testing.T) {
	const content = "abc"
	interrupted := errors.New("upload interrupted")
	for _, limit := range []int64{int64(len(content)), 16} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			store, directory := newStore(t, limit)
			reader, writer := io.Pipe()
			defer closeFile(t, reader)
			defer closeFile(t, writer)
			result := make(chan error, 1)
			go func() {
				if _, err := io.WriteString(writer, content); err != nil {
					result <- err
					return
				}
				result <- writer.CloseWithError(interrupted)
			}()
			if digest, err := store.Put(reader); digest != "" || !errors.Is(err, interrupted) {
				t.Fatalf("Put = (%q, %v), want original interruption", digest, err)
			}
			if err := <-result; err != nil {
				t.Fatal(err)
			}
			assertUnavailable(t, store, digestOf(content), fs.ErrNotExist)
			assertFiles(t, directory, 0)
		})
	}
}

func TestArtifactCannotReadOutsideStore(t *testing.T) {
	store, directory := newStore(t, 64)
	outside := filepath.Join(t.TempDir(), "outside")
	const content = "outside data"
	if err := os.WriteFile(outside, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(directory, outside)
	if err != nil {
		t.Fatal(err)
	}
	digest := digestOf(content)
	for _, name := range []string{relative, outside, ".upload-EXAMPLE", strings.ToUpper(digest), strings.Repeat("g", 64)} {
		assertUnavailable(t, store, name, artifacts.ErrInvalidDigest)
	}
	if err := os.Symlink(relative, filepath.Join(directory, digest)); err != nil {
		t.Fatal(err)
	}
	assertUnavailable(t, store, digest, fs.ErrInvalid)
	if _, err := store.Put(strings.NewReader(content)); !errors.Is(err, fs.ErrInvalid) {
		t.Fatalf("Put over a symbolic link = %v, want fs.ErrInvalid", err)
	}
	assertFiles(t, directory, 1)
}

func TestStoreValidatesConfigurationAndInput(t *testing.T) {
	for _, limit := range []int64{0, -1} {
		if _, err := artifacts.New(t.TempDir(), limit); !errors.Is(err, fs.ErrInvalid) {
			t.Fatalf("New with limit %d = %v, want fs.ErrInvalid", limit, err)
		}
	}
	if _, err := artifacts.New("", 64); !errors.Is(err, fs.ErrInvalid) {
		t.Fatalf("New with no path = %v, want fs.ErrInvalid", err)
	}
	if _, err := artifacts.New(filepath.Join(t.TempDir(), "missing"), 64); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("New with missing directory = %v, want fs.ErrNotExist", err)
	}
	store, directory := newStore(t, 64)
	if _, err := store.Put(nil); !errors.Is(err, fs.ErrInvalid) {
		t.Fatalf("Put with nil input = %v, want fs.ErrInvalid", err)
	}
	assertFiles(t, directory, 0)
}

func newStore(t *testing.T, limit int64) (*artifacts.Store, string) {
	t.Helper()
	directory := t.TempDir()
	store, err := artifacts.New(directory, limit)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeFile(t, store) })
	return store, directory
}

func put(t *testing.T, store *artifacts.Store, content string) string {
	t.Helper()
	digest, err := store.Put(strings.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

func digestOf(content string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(content)))
}

func assertContent(t *testing.T, store *artifacts.Store, digest, want string) {
	t.Helper()
	file, err := store.Open(digest)
	if err != nil {
		t.Fatal(err)
	}
	defer closeFile(t, file)
	got, err := io.ReadAll(file)
	if err != nil || string(got) != want {
		t.Fatalf("read artifact = (%q, %v), want %q", got, err, want)
	}
}

func assertUnavailable(t *testing.T, store *artifacts.Store, digest string, want error) {
	t.Helper()
	file, err := store.Open(digest)
	if file != nil {
		closeFile(t, file)
		t.Fatalf("Open(%q) returned a file", digest)
	}
	if !errors.Is(err, want) {
		t.Fatalf("Open(%q) = %v, want %v", digest, err, want)
	}
}

func assertFiles(t *testing.T, directory string, want int) {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != want {
		t.Fatalf("directory contains %v, want %d entries", entries, want)
	}
}

func closeFile(t *testing.T, closer io.Closer) {
	t.Helper()
	if err := closer.Close(); err != nil {
		t.Error(err)
	}
}
