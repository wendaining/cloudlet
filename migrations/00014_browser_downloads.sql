-- +goose Up
CREATE TABLE browser_downloads (
    id UUID PRIMARY KEY,
    session_id UUID NOT NULL REFERENCES browser_sessions (id) ON DELETE CASCADE,
    artifact_digest varchar(64) NOT NULL CHECK (artifact_digest ~ '^[0-9a-f]{64}$'),
    size_bytes bigint NOT NULL CHECK (size_bytes >= 0 AND size_bytes <= 8388608),
    filename text NOT NULL,
    created_at timestamptz NOT NULL
);
CREATE INDEX browser_downloads_session_id_idx ON browser_downloads (session_id);

-- +goose Down
DROP TABLE browser_downloads;
