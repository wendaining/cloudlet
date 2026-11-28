-- +goose Up
ALTER TABLE container_executions
    ADD COLUMN s3_binding_name TEXT,
    ADD COLUMN s3_secret_version_id UUID REFERENCES project_secret_versions (id),
    ADD CONSTRAINT container_s3_binding_reference
        FOREIGN KEY (project_id, s3_binding_name) REFERENCES s3_bindings (project_id, name),
    ADD CONSTRAINT container_s3_binding_pair
        CHECK ((s3_binding_name IS NULL) = (s3_secret_version_id IS NULL));

-- +goose Down
ALTER TABLE container_executions
    DROP COLUMN s3_secret_version_id,
    DROP COLUMN s3_binding_name;
