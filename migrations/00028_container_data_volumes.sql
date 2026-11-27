-- +goose Up
ALTER TABLE container_executions
    ADD COLUMN volume_name TEXT NOT NULL DEFAULT '',
    ADD COLUMN volume_mount_path TEXT NOT NULL DEFAULT '',
    ADD COLUMN volume_directory TEXT NOT NULL DEFAULT '',
    ADD CONSTRAINT container_executions_volume_check CHECK (
        (volume_name = '' AND volume_mount_path = '' AND volume_directory = '')
        OR
        (char_length(volume_name) BETWEEN 1 AND 255
            AND volume_name = btrim(volume_name)
            AND volume_mount_path LIKE '/%'
            AND char_length(volume_mount_path) BETWEEN 1 AND 4096
            AND volume_mount_path = btrim(volume_mount_path)
            AND char_length(volume_directory) BETWEEN 1 AND 255
            AND volume_directory = btrim(volume_directory))
    );

-- Only one running execution may write one declared volume.
CREATE UNIQUE INDEX container_executions_running_volume_idx
    ON container_executions (volume_name)
    WHERE state = 'running' AND volume_name <> '';

-- +goose Down
DROP INDEX container_executions_running_volume_idx;
ALTER TABLE container_executions
    DROP CONSTRAINT container_executions_volume_check,
    DROP COLUMN volume_directory,
    DROP COLUMN volume_mount_path,
    DROP COLUMN volume_name;
