-- +goose Up
ALTER TABLE worker_deployments DROP CONSTRAINT worker_deployments_state_check;
ALTER TABLE worker_deployments ADD CONSTRAINT worker_deployments_state_check
    CHECK (state IN ('pending', 'ready', 'active', 'retired', 'failed'));

CREATE TABLE worker_activations (
    operation_id UUID PRIMARY KEY REFERENCES operations(id) ON DELETE RESTRICT,
    project_id UUID NOT NULL,
    worker_id UUID NOT NULL,
    deployment_id UUID NOT NULL UNIQUE,
    previous_deployment_id UUID NOT NULL,
    previous_target TEXT NOT NULL,
    CHECK (deployment_id <> previous_deployment_id),
    FOREIGN KEY (project_id, worker_id, deployment_id) REFERENCES worker_deployments(project_id, worker_id, id),
    FOREIGN KEY (project_id, worker_id, previous_deployment_id) REFERENCES worker_deployments(project_id, worker_id, id)
);

-- +goose Down
DROP TABLE worker_activations;
ALTER TABLE worker_deployments DROP CONSTRAINT worker_deployments_state_check;
ALTER TABLE worker_deployments ADD CONSTRAINT worker_deployments_state_check
    CHECK (state IN ('pending', 'active', 'failed'));
