-- +goose Up
-- De dónde viene lo importado de otro servicio: el servidor, la organización
-- y el id allí. Repetir la importación busca aquí lo que ya trajo. imported
-- guarda lo que se trajo la última vez, para saber qué ha cambiado allí desde
-- entonces y no pisar lo que se haya cambiado aquí.
CREATE TABLE import_origins (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    source      text NOT NULL CHECK (source IN ('sentry')),
    server      text NOT NULL,
    org         text NOT NULL,
    kind        text NOT NULL CHECK (kind IN ('project', 'issue', 'release', 'deploy')),
    external_id text NOT NULL,
    project_id  bigint REFERENCES projects (id) ON DELETE CASCADE,
    issue_id    bigint REFERENCES issues (id) ON DELETE CASCADE,
    release_id  bigint REFERENCES releases (id) ON DELETE CASCADE,
    deploy_id   bigint REFERENCES deploys (id) ON DELETE CASCADE,
    imported    jsonb NOT NULL DEFAULT '{}',
    imported_at timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (source, server, org, kind, external_id),
    CHECK ((kind = 'project') = (project_id IS NOT NULL)),
    CHECK ((kind = 'issue') = (issue_id IS NOT NULL)),
    CHECK ((kind = 'release') = (release_id IS NOT NULL)),
    CHECK ((kind = 'deploy') = (deploy_id IS NOT NULL))
);

CREATE INDEX import_origins_issue_idx ON import_origins (issue_id) WHERE issue_id IS NOT NULL;
CREATE INDEX import_origins_project_idx ON import_origins (project_id) WHERE project_id IS NOT NULL;
CREATE INDEX import_origins_release_idx ON import_origins (release_id) WHERE release_id IS NOT NULL;
CREATE INDEX import_origins_deploy_idx ON import_origins (deploy_id) WHERE deploy_id IS NOT NULL;

-- Los cambios de estado que trae la importación van sin autor y con origen
-- import. La condición se amplía sobre la que haya, sin reescribirla, y la de
-- antes queda en el comentario para el Down.
-- +goose StatementBegin
DO $$
DECLARE
    c record;
BEGIN
    FOR c IN
        SELECT conname, pg_get_constraintdef(oid) AS def FROM pg_constraint
        WHERE conrelid = 'issue_status_changes'::regclass AND conname = 'issue_status_changes_source_check'
    LOOP
        EXECUTE format('ALTER TABLE issue_status_changes DROP CONSTRAINT %I, ADD CONSTRAINT %I CHECK (source = ''import'' OR %s)',
            c.conname, c.conname, substring(c.def FROM 7));
        EXECUTE format('COMMENT ON CONSTRAINT %I ON issue_status_changes IS %L', c.conname, c.def);
    END LOOP;
END $$;
-- +goose StatementEnd

-- +goose Down
DELETE FROM issue_status_changes WHERE source = 'import';
-- +goose StatementBegin
DO $$
DECLARE
    c record;
BEGIN
    FOR c IN
        SELECT conname, obj_description(oid, 'pg_constraint') AS def FROM pg_constraint
        WHERE conrelid = 'issue_status_changes'::regclass AND conname = 'issue_status_changes_source_check'
            AND obj_description(oid, 'pg_constraint') LIKE 'CHECK %'
    LOOP
        EXECUTE format('ALTER TABLE issue_status_changes DROP CONSTRAINT %I, ADD CONSTRAINT %I %s', c.conname, c.conname, c.def);
    END LOOP;
END $$;
-- +goose StatementEnd
DROP TABLE import_origins;
