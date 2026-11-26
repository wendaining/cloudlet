-- +goose Up
CREATE TABLE s3_bindings (
    project_id UUID NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    endpoint TEXT NOT NULL,
    region TEXT NOT NULL,
    bucket TEXT NOT NULL,
    secret_version_id UUID NOT NULL REFERENCES project_secret_versions (id),
    verified_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (project_id, name)
);

-- +goose Down
DROP TABLE s3_bindings;
