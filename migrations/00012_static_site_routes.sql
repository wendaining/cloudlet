-- +goose Up
ALTER TABLE project_routes
    ADD COLUMN static_site_version_id UUID REFERENCES static_site_versions (id),
    DROP CONSTRAINT project_routes_target_check,
    ADD CONSTRAINT project_routes_destination_check CHECK (
        (static_site_version_id IS NULL AND (target LIKE 'http://%' OR target LIKE 'https://%'))
        OR (static_site_version_id IS NOT NULL AND target = '' AND path_prefix = '/')
    );
CREATE UNIQUE INDEX project_routes_active_site_idx
    ON project_routes (resource) WHERE static_site_version_id IS NOT NULL;

-- +goose Down
DROP INDEX project_routes_active_site_idx;
WITH removed AS (
    DELETE FROM project_routes WHERE static_site_version_id IS NOT NULL RETURNING id
)
UPDATE route_snapshot SET version = version + 1 WHERE id = 1 AND EXISTS (SELECT 1 FROM removed);
ALTER TABLE project_routes
    DROP CONSTRAINT project_routes_destination_check,
    DROP COLUMN static_site_version_id,
    ADD CONSTRAINT project_routes_target_check CHECK (target LIKE 'http://%' OR target LIKE 'https://%');
