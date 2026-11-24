-- +goose Up
-- Los canales por defecto (Slack y Linear) se pueden apagar por proyecto; solo
-- cuentan mientras el proyecto no tenga reglas. Encendidos, como hasta ahora.
ALTER TABLE projects ADD COLUMN default_slack boolean NOT NULL DEFAULT true;
ALTER TABLE projects ADD COLUMN default_linear boolean NOT NULL DEFAULT true;

-- El id que se pide a Linear al crear a mano la issue de un error, para que
-- dos peticiones a la vez no creen dos.
ALTER TABLE issues ADD COLUMN linear_create_id uuid;

-- Quién creó el enlace a mano; vacío en los de los avisos.
ALTER TABLE issue_links ADD COLUMN author text;
ALTER TABLE issue_links ADD COLUMN author_kind text CHECK (author_kind IN ('user', 'token'));

-- +goose Down
ALTER TABLE issue_links DROP COLUMN author_kind;
ALTER TABLE issue_links DROP COLUMN author;
ALTER TABLE issues DROP COLUMN linear_create_id;
ALTER TABLE projects DROP COLUMN default_linear;
ALTER TABLE projects DROP COLUMN default_slack;
