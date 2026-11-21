-- +goose Up
-- Con debug ID un mapa se busca por él; sin él, por release, dist y nombre.
ALTER TABLE sourcemaps
    ADD COLUMN dist text NOT NULL DEFAULT '',
    ADD COLUMN url text NOT NULL DEFAULT '',
    ADD COLUMN debug_id uuid;

ALTER TABLE sourcemaps DROP CONSTRAINT sourcemaps_project_id_release_file_key;
CREATE UNIQUE INDEX sourcemaps_name_key ON sourcemaps (project_id, release, dist, file) WHERE debug_id IS NULL;
CREATE UNIQUE INDEX sourcemaps_debug_id_key ON sourcemaps (project_id, debug_id) WHERE debug_id IS NOT NULL;
CREATE INDEX sourcemaps_release_file_idx ON sourcemaps (project_id, release, file);

-- Trozos de la subida por trozos, hasta que se ensambla su bundle.
CREATE TABLE upload_chunks (
    sha1        text PRIMARY KEY,
    data        bytea NOT NULL,
    uploaded_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX upload_chunks_uploaded_at_idx ON upload_chunks (uploaded_at);

-- Bundles ya ensamblados, para responder a una subida repetida sin pedir los trozos.
CREATE TABLE artifact_bundles (
    project_id   bigint NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    checksum     text NOT NULL,
    release      text NOT NULL,
    dist         text NOT NULL,
    files        integer NOT NULL,
    assembled_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (project_id, checksum)
);

CREATE INDEX artifact_bundles_assembled_at_idx ON artifact_bundles (assembled_at);

-- +goose Down
DROP TABLE artifact_bundles;
DROP TABLE upload_chunks;
DROP INDEX sourcemaps_release_file_idx;
DROP INDEX sourcemaps_debug_id_key;
DROP INDEX sourcemaps_name_key;
-- Sin dist ni debug ID solo cabe un mapa por release y fichero: se queda el más nuevo.
DELETE FROM sourcemaps s
WHERE EXISTS (
    SELECT 1 FROM sourcemaps o
    WHERE o.project_id = s.project_id AND o.release = s.release AND o.file = s.file
      AND (o.uploaded_at, o.id) > (s.uploaded_at, s.id)
);
ALTER TABLE sourcemaps
    DROP COLUMN debug_id,
    DROP COLUMN url,
    DROP COLUMN dist;
ALTER TABLE sourcemaps ADD CONSTRAINT sourcemaps_project_id_release_file_key UNIQUE (project_id, release, file);
