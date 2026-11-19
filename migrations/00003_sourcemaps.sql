-- +goose Up
CREATE TABLE sourcemaps (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    project_id  bigint NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    release     text NOT NULL,
    file        text NOT NULL,
    map_gzip    bytea NOT NULL,
    size        bigint NOT NULL,
    uploaded_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (project_id, release, file)
);

CREATE INDEX sourcemaps_uploaded_at_idx ON sourcemaps (uploaded_at);

-- +goose Down
DROP TABLE sourcemaps;
