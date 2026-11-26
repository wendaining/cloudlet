-- +goose Up
CREATE TABLE postgres_bindings (
    project_id UUID NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    host TEXT NOT NULL,
    port INTEGER NOT NULL,
    database TEXT NOT NULL,
    secret_version_id UUID NOT NULL REFERENCES project_secret_versions (id),
    verified_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (project_id, name)
);

-- +goose Down
DROP TABLE postgres_bindings;
