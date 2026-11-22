-- +goose Up
-- Releases de cada proyecto: se crean con el primer evento que trae una
-- versión nueva o a mano. first_seen ordena las releases: una es posterior a
-- otra si se vio después.
CREATE TABLE releases (
    id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    project_id   bigint NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    version      text NOT NULL CHECK (version <> ''),
    first_seen   timestamptz NOT NULL DEFAULT now(),
    finalized_at timestamptz,
    UNIQUE (project_id, version)
);

CREATE INDEX releases_project_first_seen_idx ON releases (project_id, first_seen DESC, id DESC);

CREATE TABLE deploys (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    release_id  bigint NOT NULL REFERENCES releases (id) ON DELETE CASCADE,
    environment text NOT NULL CHECK (environment <> ''),
    deployed_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX deploys_release_idx ON deploys (release_id, deployed_at DESC);

-- Eventos de cada issue por release y entorno ('' si el evento no lo trae),
-- también los que no se guardan por el tope o el muestreo. regressed_at es la
-- última regresión que llegó con esa release y ese entorno.
CREATE TABLE issue_stats (
    issue_id     bigint NOT NULL REFERENCES issues (id) ON DELETE CASCADE,
    project_id   bigint NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    release      text NOT NULL,
    environment  text NOT NULL,
    first_seen   timestamptz NOT NULL,
    last_seen    timestamptz NOT NULL,
    event_count  bigint NOT NULL CHECK (event_count >= 0),
    regressed_at timestamptz,
    PRIMARY KEY (issue_id, release, environment)
);

CREATE INDEX issue_stats_project_release_idx ON issue_stats (project_id, release);
CREATE INDEX issue_stats_project_environment_idx ON issue_stats (project_id, environment);

-- first_release: la release del primer evento. fixed_in_release: la release
-- cuyo despliegue resolvió un issue que estaba en resolved_next.
ALTER TABLE issues
    ADD COLUMN first_release text,
    ADD COLUMN fixed_in_release text;

INSERT INTO releases (project_id, version, first_seen)
SELECT project_id, release, min(received_at)
FROM events WHERE release IS NOT NULL AND release <> ''
GROUP BY project_id, release
ORDER BY min(received_at);

INSERT INTO issue_stats (issue_id, project_id, release, environment, first_seen, last_seen, event_count)
SELECT issue_id, project_id, COALESCE(release, ''), COALESCE(environment, ''), min(occurred_at), max(occurred_at), count(*)
FROM events
GROUP BY issue_id, project_id, COALESCE(release, ''), COALESCE(environment, '');

UPDATE issues i SET first_release = (
    SELECT NULLIF(e.release, '') FROM events e
    WHERE e.issue_id = i.id ORDER BY e.occurred_at, e.id LIMIT 1);

-- +goose Down
ALTER TABLE issues
    DROP COLUMN fixed_in_release,
    DROP COLUMN first_release;
DROP TABLE issue_stats;
DROP TABLE deploys;
DROP TABLE releases;
