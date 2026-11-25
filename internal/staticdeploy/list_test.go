package staticdeploy

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/wendaining/cloudlet/internal/database"
)

func TestPageSize(t *testing.T) {
	if limit, err := pageSize(nil); err != nil || limit != 50 {
		t.Fatalf("default = %d, %v", limit, err)
	}
	for _, value := range []int{-1, 0, 1, 100, 101} {
		limit, err := pageSize(&value)
		switch {
		case value < 1 || value > 100:
			if !errors.Is(err, ErrInvalidPagination) {
				t.Errorf("page size %d returned %v, want ErrInvalidPagination", value, err)
			}
		case err != nil || limit != value:
			t.Errorf("page size %d = %d, %v", value, limit, err)
		}
	}
}

func TestBuildPage(t *testing.T) {
	encode := func(value string) string { return value }
	if page := buildPage([]string{}, 1, encode); len(page.Items) != 0 || page.NextCursor != nil {
		t.Fatalf("empty page = %+v", page)
	}
	if page := buildPage([]string{"a"}, 1, encode); !slices.Equal(page.Items, []string{"a"}) || page.NextCursor != nil {
		t.Fatalf("final page = %+v", page)
	}
	if page := buildPage([]string{"a", "b", "c"}, 2, encode); !slices.Equal(page.Items, []string{"a", "b"}) || page.NextCursor == nil || *page.NextCursor != "b" {
		t.Fatalf("truncated page = %+v", page)
	}
}

func TestVersionCursor(t *testing.T) {
	version := database.StaticSiteVersion{
		ID:        uuid.MustParse("577fdc9f-089f-4bd1-b399-20d7bc3713ec"),
		CreatedAt: time.Date(2026, 10, 11, 12, 13, 14, 567890000, time.FixedZone("test", 8*60*60)),
	}
	encoded := encodeVersionCursor(version)
	got, err := decodeVersionCursor(encoded)
	if err != nil || got.ID != version.ID || !got.CreatedAt.Equal(version.CreatedAt) {
		t.Fatalf("cursor position = %+v, %v", got, err)
	}
	for _, malformed := range []string{
		"", encoded[:len(encoded)-1], encoded + "=", "\n" + encoded[1:],
		encodeVersionCursor(database.StaticSiteVersion{CreatedAt: version.CreatedAt}),
		encodeVersionCursor(database.StaticSiteVersion{ID: version.ID, CreatedAt: time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)}),
	} {
		if _, err := decodeVersionCursor(malformed); !errors.Is(err, ErrInvalidPagination) {
			t.Errorf("malformed cursor %q returned %v", malformed, err)
		}
	}
}
