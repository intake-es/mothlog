-- +goose Up
-- Recuento de cada valor de etiqueta por issue: las tags del evento, las que
-- salen de contexts y del user agent, release, environment y level.
CREATE TABLE issue_tag_values (
    issue_id  bigint NOT NULL REFERENCES issues (id) ON DELETE CASCADE,
    key       text NOT NULL,
    value     text NOT NULL,
    count     bigint NOT NULL CHECK (count > 0),
    last_seen timestamptz NOT NULL,
    PRIMARY KEY (issue_id, key, value)
);

-- Para buscar por etiqueta en todos los issues y autocompletar sus valores.
CREATE INDEX issue_tag_values_key_value_idx ON issue_tag_values (key, value);

-- Clave de la instalación con que se hashean los usuarios, para no guardar
-- ni el id, ni el email ni la IP en claro.
CREATE TABLE user_hash_key (
    only_row boolean PRIMARY KEY DEFAULT true CHECK (only_row),
    key      bytea NOT NULL
);
INSERT INTO user_hash_key (key) VALUES (uuid_send(gen_random_uuid()) || uuid_send(gen_random_uuid()));

-- Usuarios distintos de cada issue: los 16 primeros bytes de
-- sha256(clave || 'id:…' | 'email:…' | 'username:…' | 'ip:…').
CREATE TABLE issue_users (
    issue_id  bigint NOT NULL REFERENCES issues (id) ON DELETE CASCADE,
    user_hash bytea NOT NULL,
    PRIMARY KEY (issue_id, user_hash)
);

ALTER TABLE issues ADD COLUMN user_count bigint NOT NULL DEFAULT 0 CHECK (user_count >= 0);

-- Lo que se puede sacar de los eventos guardados, con las reglas de Tags() y
-- UserIdent() de la ingesta; el user agent no se lee aquí.
WITH derived AS (
    SELECT e.id, e.issue_id, e.occurred_at, d.key, btrim(d.value, E' \t\n\r') AS value
    FROM events e
    CROSS JOIN LATERAL (VALUES
        ('release', e.release),
        ('environment', e.environment),
        ('level', e.data->>'level'),
        ('browser', e.data->'contexts'->'browser'->>'name'),
        ('os', e.data->'contexts'->'os'->>'name'),
        ('device', COALESCE(NULLIF(btrim(e.data->'contexts'->'device'->>'model', E' \t\n\r'), ''),
                            e.data->'contexts'->'device'->>'family')),
        ('runtime', e.data->'contexts'->'runtime'->>'name')
    ) AS d(key, value)
    WHERE btrim(d.value, E' \t\n\r') <> ''
),
-- Las 50 primeras tags del SDK por clave, sin las claves de la búsqueda ni
-- las que no podría buscar; una derivada con valor gana a la tag de su nombre.
sdk AS (
    SELECT e.id, e.issue_id, e.occurred_at, kv.key, btrim(kv.value, E' \t\n\r') AS value
    FROM events e
    CROSS JOIN LATERAL (
        SELECT k.key, k.value, row_number() OVER (ORDER BY k.key COLLATE "C") AS n
        FROM jsonb_each_text(CASE WHEN jsonb_typeof(e.data->'tags') = 'object' THEN e.data->'tags' ELSE '{}' END) k
        WHERE k.key ~ '^[A-Za-z_][A-Za-z0-9_.-]*$' AND length(k.key) <= 32
          AND lower(k.key) NOT IN ('is', 'release', 'environment', 'level', 'project', 'user')
    ) kv
    WHERE kv.n <= 50 AND btrim(kv.value, E' \t\n\r') <> ''
      AND NOT EXISTS (SELECT 1 FROM derived d WHERE d.id = e.id AND d.key = kv.key)
),
tags AS (
    SELECT * FROM derived UNION ALL SELECT * FROM sdk
)
INSERT INTO issue_tag_values (issue_id, key, value, count, last_seen)
SELECT issue_id, key, left(value, 200), count(*), max(occurred_at)
FROM tags
GROUP BY issue_id, key, left(value, 200);

INSERT INTO issue_users (issue_id, user_hash)
SELECT DISTINCT e.issue_id, substring(sha256(k.key || convert_to(u.ident, 'UTF8')) FROM 1 FOR 16)
FROM events e
CROSS JOIN user_hash_key k
CROSS JOIN LATERAL (SELECT CASE
    WHEN COALESCE(e.data->'user'->>'id', '') <> '' THEN 'id:' || (e.data->'user'->>'id')
    WHEN btrim(COALESCE(e.data->'user'->>'email', ''), E' \t\n\r') <> ''
        THEN 'email:' || lower(btrim(e.data->'user'->>'email', E' \t\n\r'))
    WHEN COALESCE(e.data->'user'->>'username', '') <> '' THEN 'username:' || (e.data->'user'->>'username')
    WHEN COALESCE(e.data->'user'->>'ip_address', '') NOT IN ('', '{{auto}}') THEN 'ip:' || (e.data->'user'->>'ip_address')
END) AS u(ident)
WHERE u.ident IS NOT NULL;

UPDATE issues i SET user_count = u.n
FROM (SELECT issue_id, count(*) AS n FROM issue_users GROUP BY issue_id) u
WHERE u.issue_id = i.id;

-- +goose Down
ALTER TABLE issues DROP COLUMN user_count;
DROP TABLE issue_users;
DROP TABLE user_hash_key;
DROP TABLE issue_tag_values;
