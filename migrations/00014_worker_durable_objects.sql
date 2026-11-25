-- +goose Up
ALTER TABLE worker_executions ADD CONSTRAINT worker_executions_project_worker_id_key UNIQUE (project_id, worker_id, id);

CREATE TABLE worker_durable_storage (
    worker_id UUID PRIMARY KEY,
    project_id UUID NOT NULL,
    volume_name TEXT NOT NULL UNIQUE,
    owner_execution_id UUID,
    UNIQUE (project_id, worker_id),
    FOREIGN KEY (project_id, worker_id) REFERENCES workers(project_id, id),
    FOREIGN KEY (project_id, worker_id, owner_execution_id) REFERENCES worker_executions(project_id, worker_id, id)
);

CREATE TABLE worker_durable_namespaces (
    id UUID PRIMARY KEY,
    project_id UUID NOT NULL,
    worker_id UUID NOT NULL,
    class_name TEXT NOT NULL,
    UNIQUE (worker_id, class_name),
    FOREIGN KEY (project_id, worker_id) REFERENCES worker_durable_storage(project_id, worker_id)
);

-- +goose Down
DROP TABLE worker_durable_namespaces;
DROP TABLE worker_durable_storage;
ALTER TABLE worker_executions DROP CONSTRAINT worker_executions_project_worker_id_key;
