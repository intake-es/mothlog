-- +goose Up
-- OAuth 2.1 del MCP. Los clientes registrados con el registro dinámico; los
-- que se identifican con una URL (Client ID Metadata Document) no se guardan.
-- Los que no llegan a usarse se borran solos.
CREATE TABLE oauth_clients (
    client_id     text PRIMARY KEY,
    name          text NOT NULL,
    redirect_uris text NOT NULL,
    created_at    timestamptz NOT NULL,
    used_at       timestamptz
);
CREATE INDEX oauth_clients_unused_idx ON oauth_clients (created_at) WHERE used_at IS NULL;

-- Una concesión: lo que una persona dejó hacer a un cliente. Sigue a la
-- persona si cambia de login y cae con ella.
CREATE TABLE oauth_grants (
    id           bigserial PRIMARY KEY,
    login        text NOT NULL REFERENCES people (login) ON UPDATE CASCADE ON DELETE CASCADE,
    client_id    text NOT NULL,
    client_name  text NOT NULL,
    redirect_uri text NOT NULL,
    scopes       text NOT NULL,
    resource     text NOT NULL,
    created_at   timestamptz NOT NULL,
    last_used_at timestamptz,
    last_used_ip text,
    revoked_at   timestamptz
);
CREATE INDEX oauth_grants_login_idx ON oauth_grants (login, created_at DESC);

-- Una petición de autorización, desde que el cliente manda a la persona al
-- consentimiento hasta que canjea el código. Del código solo el sha256.
CREATE TABLE oauth_requests (
    id              text PRIMARY KEY,
    client_id       text NOT NULL,
    client_name     text NOT NULL,
    redirect_uri    text NOT NULL,
    scopes          text NOT NULL,
    state           text NOT NULL,
    code_challenge  text NOT NULL,
    resource        text NOT NULL,
    created_at      timestamptz NOT NULL,
    expires_at      timestamptz NOT NULL,
    login           text REFERENCES people (login) ON UPDATE CASCADE ON DELETE CASCADE,
    granted_scopes  text,
    code_sha256     bytea UNIQUE,
    code_expires_at timestamptz,
    used_at         timestamptz,
    grant_id        bigint REFERENCES oauth_grants (id) ON DELETE SET NULL
);
CREATE INDEX oauth_requests_expires_idx ON oauth_requests (expires_at);

-- Tokens de acceso y de refresco, solo su sha256. Un refresco canjeado se
-- guarda con used_at hasta que caduca, para reconocer que se reusa.
CREATE TABLE oauth_tokens (
    token_sha256 bytea PRIMARY KEY,
    grant_id     bigint NOT NULL REFERENCES oauth_grants (id) ON DELETE CASCADE,
    kind         text NOT NULL CHECK (kind IN ('access', 'refresh')),
    scopes       text NOT NULL,
    created_at   timestamptz NOT NULL,
    expires_at   timestamptz NOT NULL,
    used_at      timestamptz
);
CREATE INDEX oauth_tokens_grant_idx ON oauth_tokens (grant_id);
CREATE INDEX oauth_tokens_expires_idx ON oauth_tokens (expires_at);

-- El cliente por el que una persona comentó, cambió el estado o asignó.
ALTER TABLE issue_comments ADD COLUMN author_via text;
ALTER TABLE issue_status_changes ADD COLUMN author_via text;
ALTER TABLE issue_assignment_changes ADD COLUMN author_via text;

-- +goose Down
ALTER TABLE issue_assignment_changes DROP COLUMN author_via;
ALTER TABLE issue_status_changes DROP COLUMN author_via;
ALTER TABLE issue_comments DROP COLUMN author_via;
DROP TABLE oauth_tokens;
DROP TABLE oauth_requests;
DROP TABLE oauth_grants;
DROP TABLE oauth_clients;
