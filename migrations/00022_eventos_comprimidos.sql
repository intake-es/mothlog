-- +goose Up
-- El evento se guarda comprimido con zstd en data_zstd, y lo que se consulta
-- por SQL va en columnas propias: el usuario que se enseña (user_label), el
-- que cuenta el resumen (user_ident), organization_id y el resumen de una
-- línea de la lista (summary). Los eventos de antes siguen en data hasta que
-- el servicio los convierte por lotes en segundo plano; entonces data queda a
-- NULL. Solo se añaden columnas sin valor por defecto, así que no reescribe
-- la tabla y no tarda aunque tenga muchas filas.
ALTER TABLE events
    ALTER COLUMN data DROP NOT NULL,
    ADD COLUMN data_zstd       bytea,
    ADD COLUMN user_label      text,
    ADD COLUMN user_ident      text,
    ADD COLUMN organization_id text,
    ADD COLUMN summary         text;

-- Ya va comprimido: que Postgres no intente comprimirlo otra vez.
ALTER TABLE events ALTER COLUMN data_zstd SET STORAGE EXTERNAL;

ALTER TABLE events ADD CONSTRAINT events_data_check CHECK (data IS NOT NULL OR data_zstd IS NOT NULL) NOT VALID;

-- +goose Down
-- Los eventos ya convertidos no se pueden devolver a jsonb desde SQL.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM events WHERE data IS NULL) THEN
        RAISE EXCEPTION 'hay eventos guardados comprimidos: no se puede volver a events.data sin perderlos';
    END IF;
END $$;
-- +goose StatementEnd
ALTER TABLE events
    DROP CONSTRAINT events_data_check,
    DROP COLUMN summary,
    DROP COLUMN organization_id,
    DROP COLUMN user_ident,
    DROP COLUMN user_label,
    DROP COLUMN data_zstd,
    ALTER COLUMN data SET NOT NULL;
