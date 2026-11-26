-- +goose Up
ALTER TABLE browser_sessions ADD COLUMN last_error TEXT NOT NULL DEFAULT '';
CREATE INDEX browser_sessions_project_host_list_idx
    ON browser_sessions (project_id, host_id, created_at DESC, id DESC);

-- +goose Down
DROP INDEX browser_sessions_project_host_list_idx;
ALTER TABLE browser_sessions DROP COLUMN last_error;
