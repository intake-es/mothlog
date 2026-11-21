-- +goose Up
-- Etiquetas de Linear que llevan las issues del proyecto, además de las de LINEAR_LABEL_IDS.
ALTER TABLE projects ADD COLUMN linear_label_ids text[] NOT NULL DEFAULT '{}';
ALTER TABLE projects DROP COLUMN linear_extra_label;

-- +goose Down
ALTER TABLE projects ADD COLUMN linear_extra_label boolean NOT NULL DEFAULT false;
ALTER TABLE projects DROP COLUMN linear_label_ids;
