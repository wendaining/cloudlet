package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

var ErrStaticSiteAlreadyPublished = errors.New("static site already has a publication")

// ActivateStaticSiteVersion claims a root domain and the site's first active
// version in one route transaction. Retrying an identical claim is idempotent.
func (s *Store) ActivateStaticSiteVersion(ctx context.Context, projectID string, siteID, versionID uuid.UUID, host string) (int64, error) {
	var snapshot int64
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Raw("SELECT version FROM route_snapshot WHERE id = 1 FOR UPDATE").Row().Scan(&snapshot); err != nil {
			return fmt.Errorf("lock static site route snapshot: %w", err)
		}
		var verified uuid.UUID
		if err := tx.Raw(`SELECT v.id FROM static_site_versions AS v
			JOIN static_sites AS s ON s.id = v.site_id
			WHERE s.project_id = ? AND s.id = ? AND v.id = ?`, projectID, siteID, versionID).Row().Scan(&verified); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return gorm.ErrRecordNotFound
			}
			return fmt.Errorf("verify static site version %s in project %s: %w", versionID, projectID, err)
		}
		resource := siteID.String()
		var active ProjectRoute
		err := tx.Where("project_id = ? AND resource = ? AND static_site_version_id IS NOT NULL", projectID, resource).Take(&active).Error
		switch {
		case err == nil:
			if *active.StaticSiteVersionID == versionID && active.Host == host {
				return nil
			}
			return ErrStaticSiteAlreadyPublished
		case !errors.Is(err, gorm.ErrRecordNotFound):
			return fmt.Errorf("find publication for static site %s: %w", siteID, err)
		}
		var conflict bool
		if err := tx.Raw(`SELECT EXISTS (SELECT 1 FROM project_routes
			WHERE host = ? AND (project_id <> ? OR path_prefix = '/'))`, host, projectID).Row().Scan(&conflict); err != nil {
			return fmt.Errorf("check static site hostname %q: %w", host, err)
		}
		if conflict {
			return ErrRouteConflict
		}
		route := ProjectRoute{
			ID: uuid.NewString(), ProjectID: projectID, Resource: resource,
			Host: host, PathPrefix: "/", StaticSiteVersionID: &versionID,
		}
		if err := tx.Create(&route).Error; err != nil {
			return routeWriteError("activate static site route", err)
		}
		if err := tx.Raw("UPDATE route_snapshot SET version = version + 1 WHERE id = 1 RETURNING version").Row().Scan(&snapshot); err != nil {
			return fmt.Errorf("advance static site route snapshot: %w", err)
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return 0, fmt.Errorf("activate static site version %s at %q: %w", versionID, host, err)
	}
	return snapshot, nil
}
