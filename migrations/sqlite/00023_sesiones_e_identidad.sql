-- +goose Up
-- Antes de que lower(login) sea único, los duplicados que ya haya (dos
-- personas con el mismo login sin mirar mayúsculas) pasan a login~N, con el
-- primer N libre desde 2; se queda su login la que entró antes. Lo que
-- apunta a la renombrada (asignados, comentarios, historial) la sigue.
CREATE TEMP TABLE people_renames AS
WITH RECURSIVE
    ranked AS (
        SELECT login, lower(login) AS low,
               row_number() OVER (PARTITION BY lower(login) ORDER BY first_seen_at, login) - 1 AS j
        FROM people
    ),
    dups AS (SELECT login, low, j FROM ranked WHERE j > 0),
    seq (k) AS (
        SELECT 2
        UNION ALL
        SELECT k + 1 FROM seq WHERE k <= (SELECT count(*) FROM people)
    ),
    free AS (
        SELECT g.low, s.k, row_number() OVER (PARTITION BY g.low ORDER BY s.k) AS j
        FROM (SELECT DISTINCT low FROM dups) g CROSS JOIN seq s
        WHERE NOT EXISTS (SELECT 1 FROM people p WHERE lower(p.login) = g.low || '~' || s.k)
    )
SELECT d.login AS old_login, d.login || '~' || f.k AS new_login
FROM dups d JOIN free f ON f.low = d.low AND f.j = d.j;

UPDATE people SET login = r.new_login FROM people_renames r WHERE people.login = r.old_login;
UPDATE issues SET assignee = r.new_login FROM people_renames r
WHERE issues.assignee_kind = 'user' AND issues.assignee = r.old_login;
UPDATE issue_comments SET author = r.new_login FROM people_renames r
WHERE issue_comments.author_kind = 'user' AND issue_comments.author = r.old_login;
UPDATE issue_status_changes SET author = r.new_login FROM people_renames r
WHERE issue_status_changes.author_kind = 'user' AND issue_status_changes.author = r.old_login;
UPDATE issue_assignment_changes SET author = r.new_login FROM people_renames r
WHERE issue_assignment_changes.author_kind = 'user' AND issue_assignment_changes.author = r.old_login;
UPDATE issue_assignment_changes SET from_name = r.new_login FROM people_renames r
WHERE issue_assignment_changes.from_kind = 'user' AND issue_assignment_changes.from_name = r.old_login;
UPDATE issue_assignment_changes SET to_name = r.new_login FROM people_renames r
WHERE issue_assignment_changes.to_kind = 'user' AND issue_assignment_changes.to_name = r.old_login;
DROP TABLE people_renames;

CREATE UNIQUE INDEX people_login_lower_key ON people (lower(login));

-- Las sesiones de una persona emitidas antes de esta fecha no valen: cerrar
-- sesión y `intake-errors people revoke` la ponen a ahora.
ALTER TABLE people ADD COLUMN sessions_valid_since INTEGER NOT NULL DEFAULT 0;

-- GitHub identifica a la persona por su id numérico. Las filas de antes
-- tenían el login como subject; pasan a login:<login> para no confundirse con
-- un id y se cambian por el id la primera vez que entra una cuenta con ese login.
UPDATE people SET subject = 'login:' || subject WHERE provider = 'github';

-- +goose Down
UPDATE people SET subject = substr(subject, 7) WHERE provider = 'github' AND subject LIKE 'login:%';
ALTER TABLE people DROP COLUMN sessions_valid_since;
DROP INDEX people_login_lower_key;
