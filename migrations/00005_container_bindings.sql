-- +goose Up
ALTER TABLE capacity_reservations
    ADD COLUMN container_environment TEXT,
    ADD COLUMN container_project TEXT,
    ADD COLUMN container_resource TEXT,
    ADD COLUMN container_generation BIGINT,
    ADD COLUMN container_image TEXT,
    ADD COLUMN container_id TEXT,
    ADD CONSTRAINT capacity_reservations_binding_check CHECK (
        (container_environment IS NULL AND container_project IS NULL AND container_resource IS NULL
            AND container_generation IS NULL AND container_image IS NULL)
        OR
        (container_environment IS NOT NULL AND container_project IS NOT NULL AND container_resource IS NOT NULL
            AND container_generation > 0 AND container_image IS NOT NULL
            AND char_length(container_environment) BETWEEN 1 AND 48
            AND container_environment = btrim(container_environment)
            AND char_length(container_project) BETWEEN 1 AND 48
            AND container_project = btrim(container_project)
            AND char_length(container_resource) BETWEEN 1 AND 48
            AND container_resource = btrim(container_resource)
            AND char_length(container_image) BETWEEN 1 AND 512)
    ),
    ADD CONSTRAINT capacity_reservations_container_id_check CHECK (
        container_id IS NULL
        OR (char_length(container_id) BETWEEN 1 AND 128 AND container_id = btrim(container_id))
    );

CREATE UNIQUE INDEX capacity_reservations_active_ownership_idx
    ON capacity_reservations (container_environment, container_project, container_resource, container_generation)
    WHERE state = 'active' AND container_environment IS NOT NULL;

-- +goose Down
DROP INDEX capacity_reservations_active_ownership_idx;
ALTER TABLE capacity_reservations
    DROP CONSTRAINT capacity_reservations_container_id_check,
    DROP CONSTRAINT capacity_reservations_binding_check,
    DROP COLUMN container_id,
    DROP COLUMN container_image,
    DROP COLUMN container_generation,
    DROP COLUMN container_resource,
    DROP COLUMN container_project,
    DROP COLUMN container_environment;
