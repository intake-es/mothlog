-- +goose Up
CREATE TABLE api_tokens (
    id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name         text NOT NULL CHECK (name <> ''),
    token_sha256 bytea NOT NULL UNIQUE,
    created_at   timestamptz NOT NULL DEFAULT now(),
    last_used_at timestamptz,
    revoked_at   timestamptz
);

-- El nombre identifica al token activo; uno revocado deja reutilizar el nombre.
CREATE UNIQUE INDEX api_tokens_active_name_idx ON api_tokens (name) WHERE revoked_at IS NULL;

-- +goose Down
DROP TABLE api_tokens;
