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
| `RETENTION_DAYS` | días que se guardan los eventos y los source maps | `90` |
| `SOURCEMAP_UPLOAD_TOKEN` | token para subir source maps; sin él la subida responde 401 | vacío |

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

Los tests de `internal/store`, `internal/ingest` e `internal/sourcemap` usan Postgres solo si existe `INTAKE_ERRORS_TEST_DATABASE_URL`; cada test crea su propio esquema y lo borra al acabar. Con el Postgres de `docker compose`:

```sh
INTAKE_ERRORS_TEST_DATABASE_URL='postgres://intake_errors:intake_errors@127.0.0.1:5433/intake_errors?sslmode=disable' go test ./internal/store ./internal/ingest ./internal/sourcemap
```

## Source maps del front

Los errores del panel se traducen a su TypeScript original con los `.map` del build. Se guardan por proyecto, release y nombre del `.js` (los chunks de Angular llevan un hash en el nombre, así que no hacen falta debug IDs), comprimidos, y se borran a los `RETENTION_DAYS` días de subirlos.

`POST /api/{project_id}/releases/{release}/sourcemaps/` recibe un multipart con uno o varios campos `file` (`chunk-XXXX.js.map`) y `Authorization: Bearer <SOURCEMAP_UPLOAD_TOKEN>`. Volver a subir un fichero lo sustituye. Responde con lo guardado:

```json
{"release": "2026.11.19-1", "files": [{"name": "chunk-A1B2C3D4.js.map", "file": "chunk-A1B2C3D4.js", "size": 33433}]}
```

Errores, como `{"error": "..."}`: 401 sin token o con uno malo, 404 si el proyecto no existe, 413 si un fichero pasa de 20 MiB o la petición de 100 MiB, y 400 si un fichero no es un source map v3 válido (con su nombre). Si algo falla no se guarda nada de esa petición.

En `Dockerfile` del front, en lugar de `sentry-cli`, después del build y antes de borrar los `.map` (una petición por fichero para no acercarse al límite de 100 MiB; el token, como secreto de BuildKit):

```dockerfile
RUN --mount=type=secret,id=sourcemap_upload_token,env=SOURCEMAP_UPLOAD_TOKEN \
    cd dist/app/browser && \
    for f in *.map; do \
      curl -sS --fail-with-body \
        -H "Authorization: Bearer $SOURCEMAP_UPLOAD_TOKEN" \
        -F "file=@$f" \
        "$INTAKE_ERRORS_URL/api/$INTAKE_ERRORS_PROJECT_ID/releases/$SENTRY_RELEASE/sourcemaps/" || exit 1; \
    done && \
    rm -f *.map
```

`$SENTRY_RELEASE` tiene que ser el mismo `release` que manda el SDK, y si lleva `/`, codificado como `%2F`.

Al recibir un evento con `release`, cada fotograma de un `.js` con mapa para ese proyecto y release pasa a su fichero original (`src/app/...`), con su línea, columna y 5 líneas de contexto antes y después de `sourcesContent`. `in_app` queda falso para lo que viene de `node_modules/`. La función se deja como la manda el SDK, y la posición minificada se guarda en `minified`. Sin release, sin mapa o con un mapa roto, el fotograma se queda como estaba.

La ingesta pasa cada evento por esta traducción antes de agruparlo, así que los errores del panel se agrupan por su fichero y función originales.

## Fixtures

`testdata/envelopes/` son envíos reales de sentry-python 2.57.0 (backend), 2.66.1 (integrations) y sentry.javascript.angular 11.0.0, y `testdata/sourcemaps/` el build del front con su source map. Qué es cada uno y qué debe pasar con él está en [`tools/fixtures/README.md`](tools/fixtures/README.md); `internal/event/fixtures_test.go` sigue esa tabla.

Para regrabarlos:

1. `python3 tools/fixtures/grabador.py /ruta/de/salida` deja escuchando el receptor en `127.0.0.1:8299`.
2. Desde `intake/backend`, con `PYTHONPATH=.`, `SECRET_KEY` y `SENTRY_DSN=http://<clave>@127.0.0.1:8299/1`, ejecutar `backend.py <caso>` con `fastapi`, `validacion`, `404`, `structlog-exception`, `structlog-error` o `ardiq`. Para integrations, `integrations.py` desde `intake/integrations` (las variables que necesita, en el README de los fixtures).
3. Para el front, `node tools/fixtures/front.mjs <build> <salida> tools/fixtures/fixture-error.js` con Playwright.
4. `python3 tools/fixtures/ver.py <salida>` lista los items de cada envío. Renombrar los ficheros como en la tabla, copiarlos a `testdata/envelopes/` y ajustar la tabla del test si cambia algo.
