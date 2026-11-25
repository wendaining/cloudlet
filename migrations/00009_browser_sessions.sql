-- +goose Up
CREATE TABLE browser_sessions (
    id UUID PRIMARY KEY,
    project_id UUID NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    host_id TEXT NOT NULL REFERENCES capacity_hosts (id) ON DELETE RESTRICT,
    status TEXT NOT NULL CHECK (status IN ('starting', 'ready', 'failed', 'closed')),
    created_at TIMESTAMPTZ NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL CHECK (expires_at > created_at)
);

CREATE INDEX browser_sessions_expiry_idx
    ON browser_sessions (host_id, expires_at)
    WHERE status IN ('starting', 'ready');

-- +goose Down
DROP TABLE browser_sessions;
