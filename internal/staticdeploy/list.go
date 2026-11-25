package staticdeploy

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/wendaining/cloudlet/internal/database"
)

const defaultPageSize = 50
const maxPageSize = 100
const versionCursorBytes = 8 + len(uuid.UUID{})

var ErrInvalidPagination = errors.New("invalid static site pagination")

type Page[T any] struct {
	Items      []T
	NextCursor *string
}

func (s *Service) ListSites(ctx context.Context, projectID string, limit *int, cursor *string) (Page[database.StaticSite], error) {
	size, err := pageSize(limit)
	if err != nil {
		return Page[database.StaticSite]{}, err
	}
	var after string
	if cursor != nil {
		if !validName(*cursor) {
			return Page[database.StaticSite]{}, fmt.Errorf("%w: invalid site cursor", ErrInvalidPagination)
		}
		after = *cursor
	}
	items, err := s.store.ListStaticSites(ctx, projectID, after, size+1)
	if err != nil {
		return Page[database.StaticSite]{}, err
	}
	return buildPage(items, size, func(site database.StaticSite) string { return site.Name }), nil
}

func (s *Service) ListVersions(ctx context.Context, projectID, name string, limit *int, cursor *string) (Page[database.StaticSiteVersion], error) {
	if !validName(name) {
		return Page[database.StaticSiteVersion]{}, ErrInvalidSiteName
	}
	size, err := pageSize(limit)
	if err != nil {
		return Page[database.StaticSiteVersion]{}, err
	}
	var after *database.StaticSiteVersionCursor
	if cursor != nil {
		value, err := decodeVersionCursor(*cursor)
		if err != nil {
			return Page[database.StaticSiteVersion]{}, err
		}
		after = &value
	}
	items, err := s.store.ListStaticSiteVersions(ctx, projectID, name, after, size+1)
	if err != nil {
		return Page[database.StaticSiteVersion]{}, err
	}
	return buildPage(items, size, encodeVersionCursor), nil
}

func pageSize(limit *int) (int, error) {
	if limit == nil {
		return defaultPageSize, nil
	}
	if *limit < 1 || *limit > maxPageSize {
		return 0, fmt.Errorf("%w: limit must be between 1 and %d", ErrInvalidPagination, maxPageSize)
	}
	return *limit, nil
}

func buildPage[T any](items []T, size int, encode func(T) string) Page[T] {
	if len(items) <= size {
		return Page[T]{Items: items}
	}
	cursor := encode(items[size-1])
	return Page[T]{Items: items[:size], NextCursor: &cursor}
}

func encodeVersionCursor(version database.StaticSiteVersion) string {
	var data [versionCursorBytes]byte
	binary.BigEndian.PutUint64(data[:8], uint64(version.CreatedAt.UnixMicro()))
	copy(data[8:], version.ID[:])
	return base64.RawURLEncoding.EncodeToString(data[:])
}

func decodeVersionCursor(cursor string) (database.StaticSiteVersionCursor, error) {
	var result database.StaticSiteVersionCursor
	if len(cursor) != base64.RawURLEncoding.EncodedLen(versionCursorBytes) {
		return result, fmt.Errorf("%w: invalid version cursor length", ErrInvalidPagination)
	}
	var data [versionCursorBytes]byte
	count, err := base64.RawURLEncoding.Strict().Decode(data[:], []byte(cursor))
	if err != nil {
		return result, fmt.Errorf("%w: decode version cursor: %w", ErrInvalidPagination, err)
	}
	if count != len(data) {
		return result, fmt.Errorf("%w: incomplete version cursor", ErrInvalidPagination)
	}
	result.CreatedAt = time.UnixMicro(int64(binary.BigEndian.Uint64(data[:8]))).UTC()
	copy(result.ID[:], data[8:])
	if result.ID == uuid.Nil || result.CreatedAt.Year() < 1 || result.CreatedAt.Year() > 9999 {
		return database.StaticSiteVersionCursor{}, fmt.Errorf("%w: invalid version cursor position", ErrInvalidPagination)
	}
	return result, nil
}
