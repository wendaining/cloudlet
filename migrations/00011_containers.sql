-- +goose Up
CREATE TABLE containers (
    id UUID PRIMARY KEY,
    project_id UUID NOT NULL REFERENCES projects (id) ON DELETE RESTRICT,
    name TEXT NOT NULL CHECK (char_length(name) BETWEEN 1 AND 63 AND name ~ '^[a-z0-9]+(-[a-z0-9]+)*$'),
    active_execution_id UUID,
    state TEXT NOT NULL CHECK (state IN ('pending', 'active', 'failed')),
    failure TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (project_id, name),
    UNIQUE (project_id, id)
);

CREATE TABLE container_executions (
    id UUID PRIMARY KEY,
    project_id UUID NOT NULL,
    container_id UUID NOT NULL,
    operation_id UUID NOT NULL REFERENCES operations (id) ON DELETE RESTRICT,
    generation BIGINT NOT NULL CHECK (generation > 0),
    image TEXT NOT NULL CHECK (octet_length(image) BETWEEN 1 AND 512 AND image !~ '[[:space:]]'),
    image_digest TEXT NOT NULL CHECK (image_digest ~ '^sha256:[0-9a-f]{64}$'),
    port INTEGER NOT NULL CHECK (port BETWEEN 1 AND 65535),
    hostname TEXT NOT NULL CHECK (char_length(hostname) BETWEEN 1 AND 253 AND hostname = lower(hostname) AND hostname !~ '\.$'),
    path_prefix TEXT NOT NULL CHECK (path_prefix LIKE '/%'),
    cpu_millis BIGINT NOT NULL CHECK (cpu_millis > 0),
    memory_bytes BIGINT NOT NULL CHECK (memory_bytes > 0),
    pids BIGINT NOT NULL CHECK (pids > 0),
    open_files BIGINT NOT NULL CHECK (open_files > 0),
    user_name TEXT NOT NULL CHECK (char_length(user_name) BETWEEN 1 AND 128),
    command TEXT NOT NULL DEFAULT '[]' CHECK (command LIKE '[%'),
    env TEXT NOT NULL DEFAULT '[]' CHECK (env LIKE '[%'),
    health_path TEXT NOT NULL CHECK (health_path LIKE '/%'),
    health_timeout_ms BIGINT NOT NULL CHECK (health_timeout_ms BETWEEN 1000 AND 60000),
    docker_container_id TEXT NOT NULL DEFAULT '',
    target TEXT NOT NULL DEFAULT '',
    state TEXT NOT NULL CHECK (state IN ('pending', 'running', 'stopped', 'failed')),
    failure TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (project_id, operation_id),
    UNIQUE (container_id, generation),
    UNIQUE (project_id, container_id, id),
    FOREIGN KEY (project_id, container_id) REFERENCES containers (project_id, id)
);

ALTER TABLE containers ADD CONSTRAINT containers_active_execution_fk
    FOREIGN KEY (project_id, id, active_execution_id)
    REFERENCES container_executions (project_id, container_id, id);

CREATE INDEX container_executions_container_idx
    ON container_executions (container_id, generation DESC);

-- +goose Down
ALTER TABLE containers DROP CONSTRAINT containers_active_execution_fk;
DROP TABLE container_executions;
DROP TABLE containers;
