-- +goose Up
-- Filtros, tope por issue, muestreo y límite por minuto de cada proyecto. Sin
-- fila, el proyecto no filtra nada y usa los valores por defecto.
CREATE TABLE project_filters (
    project_id                bigint PRIMARY KEY REFERENCES projects (id) ON DELETE CASCADE,
    messages                  text[] NOT NULL DEFAULT '{}',
    exception_types           text[] NOT NULL DEFAULT '{}',
    browser_extensions        boolean NOT NULL DEFAULT false,
    web_crawlers              boolean NOT NULL DEFAULT false,
    localhost                 boolean NOT NULL DEFAULT false,
    environments              text[] NOT NULL DEFAULT '{}',
    releases                  text[] NOT NULL DEFAULT '{}',
    -- NULL: el tope por defecto.
    events_per_issue_per_hour int CHECK (events_per_issue_per_hour > 0),
    sample_rate               double precision NOT NULL DEFAULT 1 CHECK (sample_rate BETWEEN 0 AND 1),
    -- NULL: RATE_LIMIT_PER_MINUTE.
    rate_limit_per_minute     int CHECK (rate_limit_per_minute > 0),
    updated_at                timestamptz NOT NULL DEFAULT now()
);

-- Eventos descartados por proyecto, motivo y día (UTC).
CREATE TABLE dropped_events (
    project_id bigint NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    reason     text NOT NULL,
    day        date NOT NULL,
    count      bigint NOT NULL CHECK (count >= 0),
    PRIMARY KEY (project_id, day, reason)
);

-- Eventos guardados del issue en la hora que empieza en stored_hour, y cuándo
-- se guardó el último: el primero de cada día se guarda aunque haya muestreo.
ALTER TABLE issues
    ADD COLUMN stored_hour timestamptz,
    ADD COLUMN stored_in_hour int NOT NULL DEFAULT 0,
    ADD COLUMN last_stored_at timestamptz;

-- +goose Down
ALTER TABLE issues
    DROP COLUMN last_stored_at,
    DROP COLUMN stored_in_hour,
    DROP COLUMN stored_hour;
DROP TABLE dropped_events;
DROP TABLE project_filters;
