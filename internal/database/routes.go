package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
)

var ErrRouteConflict = errors.New("route conflict")

type ProjectRoute struct {
	ID                  string
	ProjectID           string
	Resource            string
	Host                string
	PathPrefix          string
	Target              string
	StaticSiteVersionID *uuid.UUID
	StaticSiteDirectory string `gorm:"->"`
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

// ReplaceProjectRoutes atomically replaces one project's complete route set
// and increments the global snapshot version.
func (s *Store) ReplaceProjectRoutes(ctx context.Context, projectID string, routes []ProjectRoute) (int64, error) {
	parsedID, err := uuid.Parse(projectID)
	if err != nil {
		return 0, fmt.Errorf("replace project routes: project ID %q is invalid: %w", projectID, fs.ErrInvalid)
	}
	projectID = parsedID.String()
	records := make([]ProjectRoute, len(routes))
	copy(records, routes)
	hosts := make([]string, 0, len(records))
	for index := range records {
		record := &records[index]
		if record.ID == "" {
			record.ID = uuid.NewString()
		}
		record.ProjectID = projectID
		hosts = append(hosts, record.Host)
	}
	var version int64
	err = s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// All publishers lock the snapshot before checking host ownership or
		// changing routes. READ COMMITTED observes the previous owner's commit.
		if err := tx.Raw(`SELECT version FROM route_snapshot WHERE id = 1 FOR UPDATE`).Row().Scan(&version); err != nil {
			return fmt.Errorf("lock route snapshot version: %w", err)
		}
		if len(hosts) > 0 {
			var conflict bool
			if err := tx.Raw(`SELECT EXISTS (SELECT 1 FROM project_routes WHERE host IN ? AND project_id <> ?)`, hosts, projectID).Row().Scan(&conflict); err != nil {
				return fmt.Errorf("check project %s route host ownership: %w", projectID, err)
			}
			if conflict {
				return fmt.Errorf("replace project %s routes: host belongs to another project: %w", projectID, ErrRouteConflict)
			}
		}
		if err := tx.Exec(`DELETE FROM project_routes WHERE project_id = ?`, projectID).Error; err != nil {
			return fmt.Errorf("delete project %s routes: %w", projectID, err)
		}
		if len(records) > 0 {
			if err := tx.Table("project_routes").Create(&records).Error; err != nil {
				return routeWriteError("insert project routes", err)
			}
		}
		if err := tx.Raw(`UPDATE route_snapshot SET version = version + 1 WHERE id = 1 RETURNING version`).Row().Scan(&version); err != nil {
			return fmt.Errorf("advance route snapshot version: %w", err)
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return 0, err
	}
	return version, nil
}

// RouteSnapshot reads a consistent snapshot version and its complete route set.
func (s *Store) RouteSnapshot(ctx context.Context) (int64, []ProjectRoute, error) {
	var version int64
	var routes []ProjectRoute
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Raw(`SELECT version FROM route_snapshot WHERE id = 1 FOR SHARE`).Row().Scan(&version); err != nil {
			return fmt.Errorf("read route snapshot version: %w", err)
		}
		if err := tx.Table("project_routes AS routes").
			Select("routes.*, COALESCE(versions.directory, '') AS static_site_directory").
			Joins("LEFT JOIN static_site_versions AS versions ON versions.id = routes.static_site_version_id").
			Order("routes.host, routes.path_prefix, routes.project_id").
			Find(&routes).Error; err != nil {
			return fmt.Errorf("read project routes: %w", err)
		}
		return nil
	})
	if err != nil {
		return 0, nil, err
	}
	return version, routes, nil
}

func routeWriteError(operation string, err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return fmt.Errorf("%s: %w: %s", operation, ErrRouteConflict, pgErr.ConstraintName)
	}
	return fmt.Errorf("%s: %w", operation, err)
}
