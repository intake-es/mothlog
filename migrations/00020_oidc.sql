-- +goose Up
-- De qué proveedor viene cada persona y su identificador estable en él (el
-- login en GitHub, el sub en OIDC). login sigue siendo como se la nombra en
-- los asignados y en el historial; si dos de proveedores distintos traen el
-- mismo, la segunda lleva un sufijo.
ALTER TABLE people
    ADD COLUMN provider text,
    ADD COLUMN subject  text;

UPDATE people SET provider = CASE WHEN login = 'dev' THEN 'dev' ELSE 'github' END, subject = login;

ALTER TABLE people
    ALTER COLUMN provider SET NOT NULL,
    ALTER COLUMN subject SET NOT NULL,
    ADD CONSTRAINT people_provider_check CHECK (provider IN ('github', 'oidc', 'dev')),
    ADD CONSTRAINT people_subject_check CHECK (subject <> ''),
    ADD CONSTRAINT people_provider_subject_key UNIQUE (provider, subject);

-- +goose Down
ALTER TABLE people
    DROP CONSTRAINT people_provider_subject_key,
    DROP COLUMN subject,
    DROP COLUMN provider;
