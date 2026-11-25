-- +goose Up
CREATE TABLE workers (
    id UUID PRIMARY KEY,
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
    name TEXT NOT NULL CHECK (char_length(name) BETWEEN 1 AND 63 AND name ~ '^[a-z0-9]+(-[a-z0-9]+)*$'),
    active_deployment_id UUID,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (project_id, name),
    UNIQUE (project_id, id)
);

CREATE TABLE worker_deployments (
    id UUID PRIMARY KEY,
    project_id UUID NOT NULL,
    worker_id UUID NOT NULL,
    operation_id UUID NOT NULL REFERENCES operations(id) ON DELETE RESTRICT,
    artifact_digest TEXT NOT NULL CHECK (artifact_digest ~ '^[0-9a-f]{64}$'),
    config_digest TEXT NOT NULL CHECK (config_digest ~ '^[0-9a-f]{64}$'),
    entry TEXT NOT NULL,
    compatibility_date TEXT NOT NULL,
    runtime_version TEXT NOT NULL,
    runtime_image TEXT NOT NULL,
    hostname TEXT NOT NULL,
    cpu_millis BIGINT NOT NULL CHECK (cpu_millis > 0),
    memory_bytes BIGINT NOT NULL CHECK (memory_bytes > 0),
    state TEXT NOT NULL CHECK (state IN ('pending', 'active', 'failed')),
    failure TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (project_id, worker_id, id),
    UNIQUE (project_id, operation_id),
    FOREIGN KEY (project_id, worker_id) REFERENCES workers(project_id, id)
);

ALTER TABLE workers ADD CONSTRAINT workers_active_deployment_fk
    FOREIGN KEY (project_id, id, active_deployment_id) REFERENCES worker_deployments(project_id, worker_id, id);

CREATE TABLE worker_executions (
    id UUID PRIMARY KEY,
    project_id UUID NOT NULL,
    worker_id UUID NOT NULL,
    deployment_id UUID NOT NULL,
    generation BIGINT NOT NULL CHECK (generation > 0),
    container_id TEXT NOT NULL DEFAULT '',
    state TEXT NOT NULL CHECK (state IN ('pending', 'running', 'stopped')),
    FOREIGN KEY (project_id, worker_id, deployment_id) REFERENCES worker_deployments(project_id, worker_id, id),
    UNIQUE (worker_id, generation),
    UNIQUE (deployment_id)
);

-- +goose Down
DROP TABLE worker_executions;
ALTER TABLE workers DROP CONSTRAINT workers_active_deployment_fk;
DROP TABLE worker_deployments;
DROP TABLE workers;
