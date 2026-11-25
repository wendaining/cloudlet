package database

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type StaticSite struct {
	ID        uuid.UUID
	ProjectID string
	Name      string
	CreatedAt time.Time
}

type StaticSiteVersion struct {
	ID             uuid.UUID
	SiteID         uuid.UUID
	ArtifactDigest string
	Directory      string
	CreatedAt      time.Time
}

// CreateStaticSiteVersion atomically registers a site and its prepared version.
// uncertain indicates that commit was attempted and the directory must be retained
// so a potentially committed version can still be served.
func (s *Store) CreateStaticSiteVersion(ctx context.Context, projectID, name, digest, directory string) (version StaticSiteVersion, uncertain bool, err error) {
	err = s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		site := StaticSite{ID: uuid.New(), ProjectID: projectID, Name: name}
		insert := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "project_id"}, {Name: "name"}},
			DoNothing: true,
		}).Create(&site)
		if insert.Error != nil {
			return fmt.Errorf("register static site: %w", insert.Error)
		}
		if insert.RowsAffected == 0 {
			site = StaticSite{}
			if err := tx.First(&site, "project_id = ? AND name = ?", projectID, name).Error; err != nil {
				return fmt.Errorf("look up existing static site: %w", err)
			}
		}
		version = StaticSiteVersion{ID: uuid.New(), SiteID: site.ID, ArtifactDigest: digest, Directory: directory}
		if err := tx.Create(&version).Error; err != nil {
			return fmt.Errorf("register static site version: %w", err)
		}
		uncertain = true
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return StaticSiteVersion{}, uncertain, fmt.Errorf("create version for static site %q in project %s: %w", name, projectID, err)
	}
	return version, false, nil
}

func (s *Store) StaticSiteVersion(ctx context.Context, projectID, name string, id uuid.UUID) (StaticSiteVersion, error) {
	var version StaticSiteVersion
	err := s.DB.WithContext(ctx).
		Joins("JOIN static_sites ON static_sites.id = static_site_versions.site_id").
		Where("static_sites.project_id = ? AND static_sites.name = ? AND static_site_versions.id = ?", projectID, name, id).
		First(&version).Error
	if err != nil {
		return StaticSiteVersion{}, fmt.Errorf("read static site %q version %s in project %s: %w", name, id, projectID, err)
	}
	return version, nil
}

// ListStaticSites reads a bounded page ordered by the project's unique site name.
func (s *Store) ListStaticSites(ctx context.Context, projectID, after string, limit int) ([]StaticSite, error) {
	items := make([]StaticSite, 0)
	query := s.DB.WithContext(ctx).Where("project_id = ?", projectID)
	if after != "" {
		query = query.Where("name > ?", after)
	}
	if err := query.Order("name ASC").Limit(limit).Find(&items).Error; err != nil {
		return nil, fmt.Errorf("list static sites in project %s: %w", projectID, err)
	}
	return items, nil
}

// StaticSiteVersionCursor identifies a position in the version order.
type StaticSiteVersionCursor struct {
	CreatedAt time.Time
	ID        uuid.UUID
}

func (s *Store) ListStaticSiteVersions(ctx context.Context, projectID, name string, after *StaticSiteVersionCursor, limit int) ([]StaticSiteVersion, error) {
	var site StaticSite
	if err := s.DB.WithContext(ctx).Select("id").Where("project_id = ? AND name = ?", projectID, name).Take(&site).Error; err != nil {
		return nil, fmt.Errorf("find static site %q in project %s: %w", name, projectID, err)
	}
	items := make([]StaticSiteVersion, 0)
	query := s.DB.WithContext(ctx).Select("id, site_id, artifact_digest, created_at").Where("site_id = ?", site.ID)
	if after != nil {
		query = query.Where("(created_at, id) < (?, ?)", after.CreatedAt, after.ID)
	}
	if err := query.Order("created_at DESC, id DESC").Limit(limit).Find(&items).Error; err != nil {
		return nil, fmt.Errorf("list versions for static site %s in project %s: %w", site.ID, projectID, err)
	}
	return items, nil
}
