-- +goose Up
CREATE TABLE browser_screenshots (
    id UUID PRIMARY KEY,
    session_id UUID NOT NULL REFERENCES browser_sessions (id) ON DELETE CASCADE,
    digest TEXT NOT NULL CHECK (digest ~ '^[a-f0-9]{64}$'),
    size_bytes BIGINT NOT NULL CHECK (size_bytes BETWEEN 1 AND 2097152),
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX browser_screenshots_session_idx ON browser_screenshots (session_id);

-- +goose Down
DROP TABLE browser_screenshots;
