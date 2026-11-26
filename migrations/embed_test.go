package migrations

import (
	"io/fs"
	"strconv"
	"strings"
	"testing"
)

func TestMigrationVersions(t *testing.T) {
	names, err := fs.Glob(Files, "*.sql")
	if err != nil {
		t.Fatal(err)
	}
	versions := make(map[int64]string, len(names))
	var latest int64
	for _, name := range names {
		prefix, _, _ := strings.Cut(name, "_")
		version, err := strconv.ParseInt(prefix, 10, 64)
		if err != nil {
			t.Fatalf("invalid migration version in %s: %v", name, err)
		}
		if previous, exists := versions[version]; exists {
			t.Fatalf("duplicate migration version %d: %s and %s", version, previous, name)
		}
		versions[version] = name
		latest = max(latest, version)
	}
	if latest != Version {
		t.Fatalf("latest migration = %d, Version = %d", latest, Version)
	}
}
