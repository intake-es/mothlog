-- +goose Up
-- Sesiones de los SDK por hora de inicio, release y entorno ('' si no lo
-- traen). errored cuenta también las que acabaron en crash o anómalas.
CREATE TABLE release_sessions (
    project_id  bigint NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    release     text NOT NULL,
    environment text NOT NULL,
    hour        timestamptz NOT NULL,
    total       bigint NOT NULL DEFAULT 0 CHECK (total >= 0),
    errored     bigint NOT NULL DEFAULT 0 CHECK (errored >= 0),
    crashed     bigint NOT NULL DEFAULT 0 CHECK (crashed >= 0),
    abnormal    bigint NOT NULL DEFAULT 0 CHECK (abnormal >= 0),
    PRIMARY KEY (project_id, release, environment, hour)
);

CREATE INDEX release_sessions_project_hour_idx ON release_sessions (project_id, hour);

-- Usuarios distintos (el did con hash, como en issue_users) por release y
-- entorno, y si alguna de sus sesiones acabó en crash.
CREATE TABLE release_users (
    project_id  bigint NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    release     text NOT NULL,
    environment text NOT NULL,
    user_hash   bytea NOT NULL,
    crashed     boolean NOT NULL DEFAULT false,
    last_seen   timestamptz NOT NULL,
    PRIMARY KEY (project_id, release, environment, user_hash)
);

CREATE INDEX release_users_last_seen_idx ON release_users (last_seen);

-- Lo último que se sabe de cada sesión individual mientras puede seguir
-- llegando, para que sus actualizaciones y reenvíos no cuenten dos veces. No
-- es el recuento: se borra a las 24 horas sin noticias de la sesión.
CREATE TABLE session_states (
    project_id  bigint NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    sid         bytea NOT NULL,
    release     text NOT NULL,
    environment text NOT NULL,
    hour        timestamptz NOT NULL,
    init_seen   boolean NOT NULL,
    status      text NOT NULL,
    errors      bigint NOT NULL CHECK (errors >= 0),
    sent_at     timestamptz NOT NULL,
    user_hash   bytea,
    updated_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (project_id, sid)
);

CREATE INDEX session_states_updated_idx ON session_states (updated_at);

-- +goose Down
DROP TABLE session_states;
DROP TABLE release_users;
DROP TABLE release_sessions;
