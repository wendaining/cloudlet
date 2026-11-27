package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

var ErrStaticSiteRevisionConflict = errors.New("static site publication revision changed")

type StaticSitePublication struct {
	SiteID    uuid.UUID
	VersionID uuid.UUID
	Host      string
	Revision  time.Time
}

// StaticSitePublication reads the active route for a site in the given project.
func (s *Store) StaticSitePublication(ctx context.Context, projectID, name string) (StaticSitePublication, error) {
	var publication StaticSitePublication
	err := s.DB.WithContext(ctx).Raw(`
        SELECT sites.id, routes.static_site_version_id, routes.host, routes.updated_at
        FROM static_sites AS sites
        JOIN project_routes AS routes ON routes.project_id = sites.project_id AND routes.resource = sites.id::text
        WHERE sites.project_id = ? AND sites.name = ? AND routes.static_site_version_id IS NOT NULL`,
		projectID, name).Row().Scan(&publication.SiteID, &publication.VersionID, &publication.Host, &publication.Revision)
	if errors.Is(err, sql.ErrNoRows) {
		return StaticSitePublication{}, gorm.ErrRecordNotFound
	}
	if err != nil {
		return StaticSitePublication{}, fmt.Errorf("read active publication for site %q in project %s: %w", name, projectID, err)
	}
	return publication, nil
}

// SwitchStaticSiteVersion changes only the active version on the existing route.
func (s *Store) SwitchStaticSiteVersion(ctx context.Context, projectID string, siteID, versionID uuid.UUID, expectedRevision time.Time) (StaticSitePublication, int64, error) {
	var publication StaticSitePublication
	var snapshot int64
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Raw("SELECT version FROM route_snapshot WHERE id = 1 FOR UPDATE").Row().Scan(&snapshot); err != nil {
			return fmt.Errorf("lock static site route snapshot: %w", err)
		}
		var active ProjectRoute
		if err := tx.Where("project_id = ? AND resource = ? AND static_site_version_id IS NOT NULL", projectID, siteID.String()).Take(&active).Error; err != nil {
			return fmt.Errorf("find active static site route: %w", err)
		}
		publication = StaticSitePublication{SiteID: siteID, VersionID: *active.StaticSiteVersionID, Host: active.Host, Revision: active.UpdatedAt}
		if publication.VersionID == versionID {
			return nil
		}
		if !publication.Revision.Equal(expectedRevision) {
			return ErrStaticSiteRevisionConflict
		}
		// PostgreSQL stores microsecond precision. Every successful change
		// advances the revision, even if the clock repeats or moves backwards.
		err := tx.Raw(`UPDATE project_routes
            SET static_site_version_id = ?, updated_at = GREATEST(clock_timestamp(), updated_at + INTERVAL '1 microsecond')
            WHERE id = ? AND EXISTS (SELECT 1 FROM static_site_versions WHERE id = ? AND site_id = ?)
            RETURNING updated_at`, versionID, active.ID, versionID, siteID).Row().Scan(&publication.Revision)
		if errors.Is(err, sql.ErrNoRows) {
			return gorm.ErrRecordNotFound
		}
		if err != nil {
			return fmt.Errorf("switch active static site route: %w", err)
		}
		publication.VersionID = versionID
		if err := tx.Raw("UPDATE route_snapshot SET version = version + 1 WHERE id = 1 RETURNING version").Row().Scan(&snapshot); err != nil {
			return fmt.Errorf("advance static site route snapshot: %w", err)
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return StaticSitePublication{}, 0, fmt.Errorf("switch site %s in project %s to version %s: %w", siteID, projectID, versionID, err)
	}
	return publication, snapshot, nil
}
