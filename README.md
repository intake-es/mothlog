# intake-errors

intake-errors: recibe los envíos de los SDK de Sentry que ya usa la aplicación (solo cambia el DSN), los agrupa en issues y avisa a Slack y Linear.

## Arrancar

Hace falta Go 1.27 y Docker para el Postgres local.

```sh
make run    # levanta Postgres 17 con docker compose y arranca el servicio en :8080
make test   # go test ./...
make lint   # go vet, gofmt y golangci-lint
```

Al arrancar aplica las migraciones de `migrations/`. Configuración por entorno:

| Variable | Para qué | Por defecto |
|---|---|---|
| `DATABASE_URL` | Postgres | obligatoria |
| `LISTEN_ADDR` | dirección HTTP | `:8080` |
| `LOG_LEVEL` | `debug`, `info`, `warn` o `error` | `info` |
| `PUBLIC_URL` | URL pública del servicio, para escribir los DSN | obligatoria para `projects` |
| `RATE_LIMIT_PER_MINUTE` | eventos por minuto y proyecto | `60` |
| `RETENTION_DAYS` | días que se guardan los eventos | `90` |

`GET /healthz` responde `ok` si Postgres contesta.

## Proyectos

```sh
intake-errors projects create backend   # imprime el DSN: https://<clave>@<host de PUBLIC_URL>/<id>
intake-errors projects list
```

## Ingesta

Los SDK envían a `POST /api/<id>/envelope/` con la clave del DSN en `X-Sentry-Auth` o en `sentry_key` de la query. Se aceptan hasta 1 MiB comprimido y 10 MiB descomprimido. Solo se guardan los items `event`; el resto se acepta y se ignora. Pasado `RATE_LIMIT_PER_MINUTE`, responde 429 con `Retry-After` y `X-Sentry-Rate-Limits`; el límite se cuenta en memoria, por réplica.

Cada evento se agrupa en un issue por su huella: el `fingerprint` del SDK si lo trae; si no, el tipo de la excepción lanzada y el módulo o fichero y la función de sus fotogramas `in_app` (sin líneas); y en los logs sin excepción, el logger y el mensaje con UUIDs, hexadecimales y números cambiados por marcadores. Un evento en un issue `resolved`, o en uno `resolved_next` con otro release, lo reabre como regresión.

Cada hora se borran los eventos de más de `RETENTION_DAYS` días y los issues que se quedan sin eventos.

Los tests de `internal/store` e `internal/ingest` usan Postgres solo si existe `INTAKE_ERRORS_TEST_DATABASE_URL`; cada test crea su propio esquema y lo borra al acabar. Con el Postgres de `docker compose`:

```sh
INTAKE_ERRORS_TEST_DATABASE_URL='postgres://intake_errors:intake_errors@127.0.0.1:5433/intake_errors?sslmode=disable' go test ./internal/store ./internal/ingest
```

## Fixtures

`testdata/envelopes/` son envíos reales de sentry-python 2.57.0 (backend), 2.66.1 (integrations) y sentry.javascript.angular 11.0.0, y `testdata/sourcemaps/` el build del front con su source map. Qué es cada uno y qué debe pasar con él está en [`tools/fixtures/README.md`](tools/fixtures/README.md); `internal/event/fixtures_test.go` sigue esa tabla.

Para regrabarlos:

1. `python3 tools/fixtures/grabador.py /ruta/de/salida` deja escuchando el receptor en `127.0.0.1:8299`.
2. Desde `intake/backend`, con `PYTHONPATH=.`, `SECRET_KEY` y `SENTRY_DSN=http://<clave>@127.0.0.1:8299/1`, ejecutar `backend.py <caso>` con `fastapi`, `validacion`, `404`, `structlog-exception`, `structlog-error` o `ardiq`. Para integrations, `integrations.py` desde `intake/integrations` (las variables que necesita, en el README de los fixtures).
3. Para el front, `node tools/fixtures/front.mjs <build> <salida> tools/fixtures/fixture-error.js` con Playwright.
4. `python3 tools/fixtures/ver.py <salida>` lista los items de cada envío. Renombrar los ficheros como en la tabla, copiarlos a `testdata/envelopes/` y ajustar la tabla del test si cambia algo.
