-- +goose Up
CREATE TABLE secret_encryption_key (
    id SMALLINT PRIMARY KEY CHECK (id = 1),
    key_id TEXT NOT NULL UNIQUE CHECK (key_id ~ '^[a-f0-9]{64}$')
);

CREATE TABLE project_secrets (
    id UUID PRIMARY KEY,
    project_id UUID NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    name TEXT NOT NULL CHECK (name ~ '^[A-Za-z_][A-Za-z0-9_]{0,127}$'),
    latest_version INTEGER NOT NULL DEFAULT 0 CHECK (latest_version >= 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (project_id, name)
);
CREATE INDEX project_secrets_project_idx ON project_secrets (project_id, name);

CREATE TABLE project_secret_versions (
    id UUID PRIMARY KEY,
    secret_id UUID NOT NULL REFERENCES project_secrets (id) ON DELETE CASCADE,
    version INTEGER NOT NULL CHECK (version > 0),
    key_id TEXT NOT NULL REFERENCES secret_encryption_key (key_id),
    nonce BYTEA NOT NULL CHECK (octet_length(nonce) = 12),
    ciphertext BYTEA NOT NULL CHECK (octet_length(ciphertext) > 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (secret_id, version)
);

-- +goose Down
DROP TABLE project_secret_versions;
DROP TABLE project_secrets;
DROP TABLE secret_encryption_key;
