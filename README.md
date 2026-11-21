# intake-errors

Recibe los envíos de los SDK de Sentry (solo cambia el DSN), los agrupa en issues y avisa a Slack y Linear.

## Arrancar

Hace falta Go 1.27 y Docker para el Postgres local.

```sh
make run    # levanta Postgres 16 con docker compose y arranca el servicio en :8080
make test   # go test ./...
make lint   # go vet, gofmt y golangci-lint
```

Al arrancar aplica las migraciones de `migrations/`. Configuración por entorno:

| Variable | Para qué | Por defecto |
|---|---|---|
| `DATABASE_URL` | Postgres | obligatoria |
| `LISTEN_ADDR` | dirección HTTP | `:8080` |
| `LOG_LEVEL` | `debug`, `info`, `warn` o `error` | `info` |
| `PUBLIC_URL` | URL pública del servicio, para escribir los DSN y el enlace de los avisos | obligatoria para `projects create` y `list`; en `/api/0/projects/`, la de cada petición; sin ella, los avisos van sin enlace |
| `RATE_LIMIT_PER_MINUTE` | eventos por minuto y proyecto | `60` |
| `RETENTION_DAYS` | días que se guardan los eventos y los source maps | `90` |
| `SOURCEMAP_UPLOAD_TOKEN` | token para subir source maps; sin él la subida responde 401 | vacío |
| `SLACK_WEBHOOK_URL` | webhook entrante de Slack; sin él no se avisa en Slack | vacío |
| `LINEAR_API_KEY` | clave de la API de Linear; sin ella no se crean issues en Linear | vacío |
| `LINEAR_TEAM_ID` | equipo de Linear donde se crean las issues | obligatoria con `LINEAR_API_KEY` |
| `LINEAR_LABEL_IDS` | ids de etiquetas de Linear para todas las issues, separados por comas | vacío |
| `LINEAR_API_URL` | endpoint GraphQL de Linear; solo para probar contra uno falso | `https://api.linear.app/graphql` |
| `GITHUB_CLIENT_ID`, `GITHUB_CLIENT_SECRET` | la OAuth App de GitHub del login de la interfaz | vacías: sin login |
| `GITHUB_ORG` | org cuyos miembros activos pueden entrar | vacía |
| `SESSION_SECRET` | firma la cookie de sesión; 32 caracteres o más, obligatoria con GitHub | vacía |
| `UI_DEV_LOGIN` | `1` entra en la interfaz como `dev` sin GitHub; solo con `LISTEN_ADDR` en `127.0.0.1` o `localhost` | vacía |

`GET /healthz` responde `ok` si Postgres contesta.

## Interfaz

El binario sirve en `/` la interfaz para ver, entender y resolver los errores: la lista con filtros, búsqueda y la gráfica de 14 días de cada uno, y el detalle (`/issues/<id>`) con la cadena de excepciones, los breadcrumbs, las etiquetas, los eventos y «Copiar para Claude», que copia el `stacktrace.md`. Es la app de Angular de `web/`, compilada dentro del binario con `go:embed` (`internal/ui`); no pide nada a terceros, ni la fuente.

La interfaz está en español y en inglés: empieza en español si el idioma del navegador empieza por `es` y en inglés en el resto, y se cambia desde el raíl, junto al tema, sin recargar; la elección se guarda en el navegador. Los textos están en `web/src/app/core/i18n/` (`es.ts` y `en.ts`, con las mismas claves); lo que viene del servidor, como los mensajes de error de la API o el Markdown de `stacktrace.md`, no se traduce.

```sh
cd web && npm ci && npm run build   # deja la app en internal/ui/dist/app; luego go build
npm run lint && npm test            # Biome y Vitest
npm start                           # ng serve en :4200, con /api y /auth contra 127.0.0.1:8080 (arrancado con PUBLIC_URL=http://localhost:4200)
```

Sin compilar la web, `go build` y `go test` funcionan y `/` es una página que explica cómo compilarla. El `Dockerfile` compila la web antes que Go. Los ficheros con hash van con un año de caché; `index.html`, sin caché y con CSP.

Para entrar hace falta una OAuth App de GitHub con callback `<PUBLIC_URL>/auth/github/callback`. `/auth/github/login` pide el scope `read:org` con un `state` en una cookie firmada, y el callback deja entrar solo si `GET /user/memberships/orgs/<GITHUB_ORG>` dice `active`. La sesión es una cookie firmada con HMAC (`SESSION_SECRET`): `HttpOnly`, `SameSite=Lax`, `Secure` si `PUBLIC_URL` es https, de 7 días, sin tabla; `POST /auth/logout` la borra. Del usuario de GitHub solo se guarda en la cookie el login y el nombre; el token de GitHub no se guarda.

La cookie vale para `/api/0/` y `/mcp` igual que un token (los tokens siguen igual), y en lo que cambia algo exige que `Origin` sea el de `PUBLIC_URL`; si no, 403. Sin las variables de GitHub ni `UI_DEV_LOGIN`, la interfaz dice que el login no está configurado.

Para probar en local:

```sh
UI_DEV_LOGIN=1 LISTEN_ADDR=127.0.0.1:8080 DATABASE_URL=... intake-errors
```

Sin `PUBLIC_URL`, la interfaz se puede abrir por `http://127.0.0.1:8080` o por `http://localhost:8080`: en el modo de desarrollo valen las dos como `Origin`. Con `UI_DEV_LOGIN=1` y otra `LISTEN_ADDR` (por ejemplo `:8080`), el binario se niega a arrancar.

## Proyectos

```sh
intake-errors projects create backend   # imprime el DSN: https://<clave>@<host de PUBLIC_URL>/<id>
intake-errors projects list
intake-errors projects labels backend                 # enseña sus etiquetas de Linear propias
intake-errors projects labels backend <id>,<id>       # las sustituye por estas
intake-errors projects labels backend --clear         # las quita
```

Las issues de Linear de un proyecto llevan dos grupos de etiquetas: las de `LINEAR_LABEL_IDS`, que llevan las issues de todos los proyectos, y las propias del proyecto, que se ponen con `projects labels` y sirven para lo que cada uno quiera (el equipo que se encarga, una prioridad, que las recoja otro servicio…). Los ids son los de Linear, no los nombres de las etiquetas.

## Avisos

Un error nuevo, o una regresión (un evento que reabre un issue resuelto), avisa en cada canal configurado. Un evento más de un issue abierto no avisa.

- **Slack**: un mensaje de Block Kit con el tipo de aviso, el proyecto y el entorno (y, en una regresión, cuándo se resolvió); el error; la línea que falla (el fotograma propio más cercano al error del último evento, con su línea de código); el release, el espacio (`organization_name` u `organization_id`), el número de eventos en una regresión y el id del usuario o la petición sin query string; y botones a `<PUBLIC_URL>/issues/<id>` y a la issue de Linear. Si hay Linear, el aviso de Slack espera hasta 2 minutos a que se envíe el de Linear para llevar su botón.
- **Linear**: con un error nuevo, una issue `[<proyecto>] <título>` en `LINEAR_TEAM_ID` con las etiquetas de `LINEAR_LABEL_IDS` más las del proyecto, sin repetir; su descripción es el enlace a la interfaz y el Markdown de `stacktrace.md`. Con una regresión, esa misma issue vuelve al primer estado de tipo `unstarted` del equipo (sea cual sea su nombre), recibe las etiquetas del proyecto que le falten y un comentario con el release y la fecha; si se ha borrado en Linear, se crea otra. Hay una issue de Linear por error.

La ingesta deja los avisos en `alert_outbox` dentro de su transacción, así que un evento que no se guarda no avisa, y si encolar falla, el evento se guarda igual. Cada 5 segundos se envía lo pendiente; cada réplica coge filas con `FOR UPDATE SKIP LOCKED`, así que ninguna manda lo que ya está mandando otra, y los avisos de un mismo issue y canal salen en orden. Si un envío falla se reintenta al cabo de 1 min, 5 min, 30 min, 2 h, 6 h, 12 h y luego cada 24 h; tras 10 intentos se abandona, con el último error en `last_error`.

## API y MCP

La API REST (`/api/0/`) y el servidor MCP (`/mcp`) piden un token en `Authorization: Bearer <token>`. Los tokens se gestionan con el mismo binario y la misma `DATABASE_URL`:

```sh
intake-errors tokens create claude   # imprime el token (ie_…) una sola vez; en la base solo queda su sha256
intake-errors tokens list            # nombre, alta, último uso y revocación
intake-errors tokens revoke claude
```

Para conectarlo con Claude Code:

```sh
claude mcp add --transport http intake-errors https://<host>/mcp --header "Authorization: Bearer <token>"
```

Herramientas del MCP: `list_issues`, `get_issue`, `get_stacktrace`, `list_events` y `set_issue_status`. Hacen las mismas consultas que la API:

| Ruta | Qué devuelve |
|---|---|
| `GET /api/0/projects/` | id, nombre, DSN y etiquetas de Linear propias (`linear_label_ids`) de cada proyecto |
| `GET /api/0/issues/?project=&status=&query=&sort=&cursor=&limit=&daily=` | issues; `status` es `open` (por defecto), `resolved`, `resolved_next`, `silenced` o `all`; `sort`, `last_seen` (por defecto), `first_seen` o `count`; `query` busca en título y culprit; `project`, id o nombre; con `daily=1`, cada issue lleva `daily`, sus eventos por día de los últimos 14 días como en el detalle |
| `GET /api/0/issues/{id}/` | el issue con su último evento, los eventos por día de los últimos 14 días y los valores más frecuentes de entorno, release, usuario y `organization_id` |
| `GET /api/0/issues/{id}/events/` | sus eventos, del más nuevo al más viejo |
| `GET /api/0/issues/{id}/stacktrace.md` | el último evento en Markdown, pensado para un modelo |
| `POST /api/0/issues/{id}/status` | cambia el estado con `{"status": "open\|resolved\|resolved_next\|silenced"}`; `resolved_next` guarda el release del último evento (409 si no tiene) |
| `GET /api/0/events/{id}/` | el evento normalizado entero, por id o por `event_id` |

Las listas devuelven `{"data": [...], "next_cursor": "..."}`: para la página siguiente, se pasa `cursor=<next_cursor>`; `limit` va de 1 a 100 (25 por defecto). Los errores son `{"error": "..."}`, con 401 si falta el token, no existe o está revocado.


## Ingesta

Los SDK envían a `POST /api/<id>/envelope/` con la clave del DSN en `X-Sentry-Auth` o en `sentry_key` de la query. Se aceptan hasta 1 MiB comprimido y 10 MiB descomprimido. Solo se guardan los items `event`; el resto se acepta y se ignora. Pasado `RATE_LIMIT_PER_MINUTE`, responde 429 con `Retry-After` y `X-Sentry-Rate-Limits`; el límite se cuenta en memoria, por réplica.

Cada evento se agrupa en un issue por su huella: el `fingerprint` del SDK si lo trae; si no, el tipo de la excepción lanzada y el módulo o fichero y la función de sus fotogramas `in_app` (sin líneas); y en los logs sin excepción, el logger y el mensaje con UUIDs, hexadecimales y números cambiados por marcadores. Un evento en un issue `resolved`, o en uno `resolved_next` con otro release, lo reabre como regresión.

Cada hora se borran los eventos de más de `RETENTION_DAYS` días y los issues que se quedan sin eventos.

Los tests de `internal/store`, `internal/ingest`, `internal/sourcemap`, `internal/api`, `internal/alert` y de los subcomandos usan Postgres solo si existe `INTAKE_ERRORS_TEST_DATABASE_URL`; cada test crea su propio esquema y lo borra al acabar. Con el Postgres de `docker compose`:

```sh
INTAKE_ERRORS_TEST_DATABASE_URL='postgres://intake_errors:intake_errors@127.0.0.1:5433/intake_errors?sslmode=disable' go test ./...
```

## Source maps del front

Los errores del navegador se traducen a su TypeScript original con los `.map` del build. Se guardan por proyecto, release y nombre del `.js` (los chunks de un build de producción llevan un hash en el nombre, así que no hacen falta debug IDs), comprimidos, y se borran a los `RETENTION_DAYS` días de subirlos.

`POST /api/{project_id}/releases/{release}/sourcemaps/` recibe un multipart con uno o varios campos `file` (`chunk-XXXX.js.map`) y `Authorization: Bearer <SOURCEMAP_UPLOAD_TOKEN>`. Volver a subir un fichero lo sustituye. Responde con lo guardado:

```json
{"release": "2026.11.19-1", "files": [{"name": "chunk-Z554BZQ6.js.map", "file": "chunk-Z554BZQ6.js", "size": 6177}]}
```

Errores, como `{"error": "..."}`: 401 sin token o con uno malo, 404 si el proyecto no existe, 413 si un fichero pasa de 20 MiB o la petición de 100 MiB, y 400 si un fichero no es un source map v3 válido (con su nombre). Si algo falla no se guarda nada de esa petición.

Cómo subirlos en el build, en [Desplegar](#desplegar).

Al recibir un evento con `release`, cada fotograma de un `.js` con mapa para ese proyecto y release pasa a su fichero original (`src/...`), con su línea, columna y 5 líneas de contexto antes y después de `sourcesContent`. `in_app` queda falso para lo que viene de `node_modules/`. La función se deja como la manda el SDK, y la posición minificada se guarda en `minified`. Sin release, sin mapa o con un mapa roto, el fotograma se queda como estaba.

La ingesta pasa cada evento por esta traducción antes de agruparlo, así que los errores del navegador se agrupan por su fichero y función originales.

## Fixtures

`testdata/envelopes/` son envíos grabados de sentry-python 2.75.0 y `@sentry/browser` 11.15.0 desde dos apps de ejemplo (una de FastAPI y una página TypeScript compilada con esbuild), y `testdata/sourcemaps/` el build de la página con sus mapas. Las apps, cómo regrabarlos y qué debe pasar con cada envío están en [`tools/fixtures/README.md`](tools/fixtures/README.md); `internal/event/fixtures_test.go` sigue esa tabla.
