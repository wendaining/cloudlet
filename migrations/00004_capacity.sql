-- +goose Up
CREATE TABLE capacity_hosts (
    id TEXT PRIMARY KEY CHECK (char_length(btrim(id)) BETWEEN 1 AND 64 AND id = btrim(id)),
    cpu_millis BIGINT NOT NULL CHECK (cpu_millis > 0),
    memory_bytes BIGINT NOT NULL CHECK (memory_bytes > 0),
    instances BIGINT NOT NULL CHECK (instances > 0),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE capacity_project_quotas (
    project_id UUID PRIMARY KEY REFERENCES projects (id) ON DELETE CASCADE,
    cpu_millis BIGINT NOT NULL CHECK (cpu_millis > 0),
    memory_bytes BIGINT NOT NULL CHECK (memory_bytes > 0),
    instances BIGINT NOT NULL CHECK (instances > 0),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE capacity_reservations (
    execution_id UUID PRIMARY KEY,
    host_id TEXT NOT NULL REFERENCES capacity_hosts (id) ON DELETE RESTRICT,
    project_id UUID NOT NULL REFERENCES projects (id) ON DELETE RESTRICT,
    cpu_millis BIGINT NOT NULL CHECK (cpu_millis > 0),
    memory_bytes BIGINT NOT NULL CHECK (memory_bytes > 0),
    instances BIGINT NOT NULL CHECK (instances > 0),
    state TEXT NOT NULL CHECK (state IN ('active', 'released')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    released_at TIMESTAMPTZ,
    CHECK ((state = 'active' AND released_at IS NULL) OR (state = 'released' AND released_at IS NOT NULL))
);

CREATE INDEX capacity_reservations_active_host_idx ON capacity_reservations (host_id) WHERE state = 'active';
CREATE INDEX capacity_reservations_active_project_idx ON capacity_reservations (project_id) WHERE state = 'active';

-- +goose Down
DROP TABLE capacity_reservations;
DROP TABLE capacity_project_quotas;
DROP TABLE capacity_hosts;
