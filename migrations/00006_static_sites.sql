-- +goose Up
CREATE TABLE static_sites (
    id UUID PRIMARY KEY,
    project_id UUID NOT NULL REFERENCES projects (id),
    name TEXT NOT NULL CHECK (char_length(name) BETWEEN 1 AND 63 AND name ~ '^[a-z0-9]+(-[a-z0-9]+)*$'),
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (project_id, name)
);

CREATE TABLE static_site_versions (
    id UUID PRIMARY KEY,
    site_id UUID NOT NULL REFERENCES static_sites (id),
    artifact_digest TEXT NOT NULL CHECK (artifact_digest ~ '^[a-f0-9]{64}$'),
    directory TEXT NOT NULL UNIQUE CHECK (directory ~ '^\.site-[0-9]+$'),
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX static_site_versions_site_idx ON static_site_versions (site_id, created_at, id);

-- +goose Down
DROP TABLE static_site_versions;
DROP TABLE static_sites;
