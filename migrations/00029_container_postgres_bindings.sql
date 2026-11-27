-- +goose Up
ALTER TABLE container_executions
    ADD COLUMN postgres_binding_name TEXT,
    ADD COLUMN postgres_secret_version_id UUID REFERENCES project_secret_versions (id),
    ADD CONSTRAINT container_postgres_binding_reference
        FOREIGN KEY (project_id, postgres_binding_name) REFERENCES postgres_bindings (project_id, name),
    ADD CONSTRAINT container_postgres_binding_pair
        CHECK ((postgres_binding_name IS NULL) = (postgres_secret_version_id IS NULL));

-- +goose Down
ALTER TABLE container_executions
    DROP COLUMN postgres_secret_version_id,
    DROP COLUMN postgres_binding_name;
