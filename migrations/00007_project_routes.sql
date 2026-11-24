-- +goose Up
CREATE TABLE route_snapshot (
    id SMALLINT PRIMARY KEY CHECK (id = 1),
    version BIGINT NOT NULL CHECK (version >= 0)
);
INSERT INTO route_snapshot (id, version) VALUES (1, 0);

CREATE TABLE project_routes (
    id UUID PRIMARY KEY,
    project_id UUID NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    resource TEXT NOT NULL CHECK (octet_length(resource) BETWEEN 1 AND 128),
    host TEXT NOT NULL CHECK (char_length(host) BETWEEN 1 AND 253 AND host = lower(host) AND host !~ '\.$'),
    path_prefix TEXT NOT NULL CHECK (path_prefix LIKE '/%'),
    target TEXT NOT NULL CHECK (target LIKE 'http://%' OR target LIKE 'https://%'),
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (host, path_prefix)
);
CREATE INDEX project_routes_project_idx ON project_routes (project_id);

-- +goose Down
DROP TABLE project_routes;
DROP TABLE route_snapshot;
