-- +goose Up
-- Cuándo se declaró la release con un token: al crearla, finalizarla o
-- desplegarla con sentry-cli, la API o la línea de órdenes, o al importarla.
-- Las que solo ha visto la ingesta se quedan sin él, y un despliegue o un
-- finalize sin proyectos no les llega. Las de antes cuentan como declaradas
-- si están finalizadas, tienen despliegues o se importaron.
ALTER TABLE releases ADD COLUMN declared_at INTEGER;
UPDATE releases SET declared_at = COALESCE(finalized_at, first_seen)
WHERE finalized_at IS NOT NULL
   OR EXISTS (SELECT 1 FROM deploys d WHERE d.release_id = releases.id)
   OR EXISTS (SELECT 1 FROM import_origins o WHERE o.release_id = releases.id);

-- +goose Down
ALTER TABLE releases DROP COLUMN declared_at;
