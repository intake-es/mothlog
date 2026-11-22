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
| `RATE_LIMIT_PER_MINUTE` | eventos por minuto y proyecto, en los proyectos que no ponen el suyo | `60` |
| `RETENTION_DAYS` | días que se guardan los eventos y los source maps | `90` |
| `SOURCEMAP_UPLOAD_TOKEN` | token para subir source maps, además de los tokens de la API; sin él solo valen esos | vacío |
| `SLACK_WEBHOOK_URL` | webhook entrante de Slack; sin él no se avisa en Slack | vacío |
| `LINEAR_API_KEY` | clave de la API de Linear; sin ella no se crean issues en Linear | vacío |
| `LINEAR_TEAM_ID` | equipo de Linear donde se crean las issues | obligatoria con `LINEAR_API_KEY` |
| `LINEAR_LABEL_IDS` | ids de etiquetas de Linear para todas las issues, separados por comas | vacío |
| `LINEAR_API_URL` | endpoint GraphQL de Linear; solo para probar contra uno falso | `https://api.linear.app/graphql` |
| `SMTP_URL` | servidor de correo de las reglas que avisan por correo: `smtp://usuario:clave@host:587` (STARTTLS si el servidor lo ofrece) o `smtps://…:465` | vacío: sin correo |
| `SMTP_FROM` | remitente de esos correos, como `intake-errors <avisos@ejemplo.com>` | obligatoria con `SMTP_URL` |
| `GITHUB_CLIENT_ID`, `GITHUB_CLIENT_SECRET` | la OAuth App de GitHub del login de la interfaz | vacías: sin login |
| `GITHUB_ORG` | org cuyos miembros activos pueden entrar | vacía |
| `SESSION_SECRET` | firma la cookie de sesión; 32 caracteres o más, obligatoria con GitHub | vacía |
| `UI_DEV_LOGIN` | `1` entra en la interfaz como `dev` sin GitHub; solo con `LISTEN_ADDR` en `127.0.0.1` o `localhost` | vacía |

`GET /healthz` responde `ok` si Postgres contesta.

## Interfaz

El binario sirve en `/` la interfaz para ver, entender y resolver los errores: la lista con filtros, búsqueda y la gráfica de 14 días de cada uno, el detalle (`/issues/<id>`) con la cadena de excepciones, los breadcrumbs, las etiquetas, los eventos, su primera y última release y «Copiar para Claude», que copia el `stacktrace.md`, y las releases (`/releases`) con lo que trajo cada una. El selector de entorno del raíl filtra todas las pantallas ([Releases, despliegues y entornos](#releases-despliegues-y-entornos)). Es la app de Angular de `web/`, compilada dentro del binario con `go:embed` (`internal/ui`); no pide nada a terceros, ni la fuente.

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
intake-errors projects filters backend                # enseña sus filtros, el tope por issue y el muestreo
intake-errors projects filters backend set <clave> [<valor>…]   # cambia uno; ver Filtros, tope y muestreo
intake-errors projects dropped backend [<días>]       # lo descartado por motivo, 30 días por defecto
intake-errors projects rules backend [<orden> …]      # sus reglas de aviso; ver Reglas de aviso
```

Las issues de Linear de un proyecto llevan dos grupos de etiquetas: las de `LINEAR_LABEL_IDS`, que llevan las issues de todos los proyectos, y las propias del proyecto, que se ponen con `projects labels` y sirven para lo que cada uno quiera (el equipo que se encarga, una prioridad, que las recoja otro servicio…). Los ids son los de Linear, no los nombres de las etiquetas.

## Avisos

Un proyecto sin [reglas de aviso](#reglas-de-aviso) avisa así: un error nuevo, o una regresión (un evento que reabre un issue resuelto), avisa en cada canal configurado con las variables de entorno. Un evento más de un issue abierto no avisa.

- **Slack**: un mensaje de Block Kit con el tipo de aviso, el proyecto y el entorno (y, en una regresión, cuándo se resolvió); el error; la línea que falla (el fotograma propio más cercano al error del último evento, con su línea de código); el release, el espacio (`organization_name` u `organization_id`), el número de eventos en una regresión y el id del usuario o la petición sin query string; y botones a `<PUBLIC_URL>/issues/<id>` y a la issue de Linear. Si hay Linear, el aviso de Slack espera hasta 2 minutos a que se envíe el de Linear para llevar su botón.
- **Linear**: con un error nuevo, una issue `[<proyecto>] <título>` en `LINEAR_TEAM_ID` con las etiquetas de `LINEAR_LABEL_IDS` más las del proyecto, sin repetir; su descripción es el enlace a la interfaz y el Markdown de `stacktrace.md`. Con una regresión, esa misma issue vuelve al primer estado de tipo `unstarted` del equipo (sea cual sea su nombre), recibe las etiquetas del proyecto que le falten y un comentario con el release y la fecha; si se ha borrado en Linear, se crea otra. Hay una issue de Linear por error.

La ingesta deja los avisos en `alert_outbox` dentro de su transacción, así que un evento que no se guarda no avisa, y si encolar falla, el evento se guarda igual. Cada 5 segundos se envía lo pendiente; cada réplica coge filas con `FOR UPDATE SKIP LOCKED`, así que ninguna manda lo que ya está mandando otra, y los avisos de un mismo issue y canal salen en orden. Si un envío falla se reintenta al cabo de 1 min, 5 min, 30 min, 2 h, 6 h, 12 h y luego cada 24 h; tras 10 intentos se abandona, con el último error en `last_error`.

### Reglas de aviso

Con reglas, el proyecto deja de avisar como arriba y avisa de lo que digan sus reglas, a donde digan; si todas están desactivadas, no avisa. Se editan en la interfaz (Ajustes del proyecto → Avisos), con la API o con la CLI. Una regla salta cuando se cumple alguna de sus condiciones y todos sus filtros:

- **Condiciones**: error nuevo (`new`), regresión (`regressed`), más de N eventos en una ventana de hasta 24 h (`frequency`, contando también los eventos que no se guardan por el tope o el muestreo) y más de N usuarios afectados (`users`). Las dos últimas cuentan, por regla, solo los eventos que pasan sus filtros, y avisan una vez por cruce: la primera vez que se pueda (fuera del límite de la regla) mientras siga por encima del umbral; la frecuencia vuelve a avisar cuando baja del umbral y lo pasa otra vez. Un issue silenciado no avisa.
- **Filtros**: entornos, niveles (`fatal`, `error`, `warning`, `info`, `debug`) y etiquetas, con valor o solo con que estén.
- **Acciones**: Slack (su webhook), Linear (equipo y etiquetas; sin ellos, `LINEAR_TEAM_ID` y `LINEAR_LABEL_IDS`; con `LINEAR_API_KEY`), correo (`SMTP_URL`), Discord (su webhook), Telegram (token del bot y chat), ntfy (URL del tema y, si lo pide, token) y el webhook genérico. Los avisos de Slack y Linear son como los de arriba; en Linear, la frecuencia y los usuarios comentan en la issue sin cambiar su estado.
- **Límite**: `throttle_minutes`, lo menos que pasa entre dos avisos de la misma regla por el mismo issue.

Todo sale por `alert_outbox`, con los mismos reintentos. Cada réplica recuerda 30 segundos si un proyecto tiene reglas, para no consultarlo con cada evento: lo que se guarda por la API de esa réplica vale al momento, y lo de otra réplica o de la CLI, en 30 segundos como mucho. «Probar» manda en el momento, sin pasar por la tabla, un aviso de prueba por una acción de la regla guardada. Las URL de los webhooks y de los temas de ntfy y los tokens no se devuelven enteros: al editar, dejarlos como llegan (o vacíos) los conserva. El secreto del webhook genérico, si no se pone, se genera y se devuelve entero solo al guardar.

`users` cuenta, de momento, los usuarios distintos (id, correo, nombre o IP) desde que existe la regla, no los de antes; cuando cada issue lleve su cuenta de usuarios afectados, usará esa.

```sh
intake-errors projects rules backend                          # lista las reglas
intake-errors projects rules backend create regla.json        # o - para leerla de stdin
intake-errors projects rules backend edit <id> regla.json
intake-errors projects rules backend test <id> [<acción>]     # aviso de prueba por una acción o por todas
intake-errors projects rules backend delete <id>
```

```json
{
  "name": "Pagos en producción",
  "conditions": {"new": true, "regressed": true, "frequency": {"events": 100, "window_minutes": 10},
                 "environments": ["production"], "tags": [{"key": "area", "value": "pagos"}]},
  "actions": [{"type": "webhook", "url": "https://agente.ejemplo.com/avisos"},
              {"type": "email", "to": ["guardia@ejemplo.com"]}],
  "throttle_minutes": 30
}
```

#### El webhook genérico

Un `POST` con `Content-Type: application/json`:

```json
{
  "type": "new",
  "delivery": "outbox-812",
  "sent_at": "2026-11-21T10:00:00Z",
  "rule": {"id": 3, "name": "Pagos en producción"},
  "project": "backend",
  "issue": {"id": 42, "title": "TypeError: …", "culprit": "…", "level": "error", "status": "open", "count": 1,
            "first_seen": "…", "last_seen": "…", "url": "https://errores.ejemplo.com/issues/42"},
  "event": {"event_id": "…", "timestamp": "…", "level": "error", "release": "…", "environment": "production",
            "error": "TypeError: …", "location": "app/pagos.py:88 en cobrar", "user": "…", "request": "POST /pagos", "tags": {}},
  "trigger": {"events": 100, "window_minutes": 10},
  "markdown": "# TypeError: …"
}
```

`type` es `new`, `regressed`, `frequency`, `users` o `test`; `trigger` solo va en `frequency` y `users`, y `resolved_at` en las regresiones; `markdown` es el `stacktrace.md`, listo para un agente; `delivery` es el mismo en los reintentos de un aviso. Lo acompañan `X-Intake-Errors-Event` (el tipo), `X-Intake-Errors-Delivery` y `X-Intake-Errors-Signature: t=<segundos Unix>,v1=<firma>`, donde la firma es el HMAC-SHA256 en hexadecimal, con el secreto de la acción, de `<t>.<cuerpo>`. Para comprobarla, con el cuerpo tal cual llega, sin volver a serializarlo:

```python
import hashlib, hmac, time

def firma_valida(secreto: str, cabecera: str, cuerpo: bytes, margen: int = 300) -> bool:
    partes = dict(p.split("=", 1) for p in cabecera.split(","))
    t, v1 = partes.get("t", ""), partes.get("v1", "")
    if not t.isdigit() or abs(time.time() - int(t)) > margen:
        return False  # sin fecha o demasiado vieja: puede ser una repetición
    esperada = hmac.new(secreto.encode(), f"{t}.".encode() + cuerpo, hashlib.sha256).hexdigest()
    return hmac.compare_digest(esperada, v1)
```

Un 2xx da el aviso por entregado; otra respuesta o ninguna en 15 segundos, por fallido, y se reintenta.

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

Herramientas del MCP: `list_issues`, `get_issue`, `get_issue_tags`, `get_stacktrace`, `list_events` y `set_issue_status`; las de leer aceptan `environment`, como la API. Hacen las mismas consultas que la API:

| Ruta | Qué devuelve |
|---|---|
| `GET /api/0/projects/` | id, nombre, DSN y etiquetas de Linear propias (`linear_label_ids`) de cada proyecto |
| `GET /api/0/issues/?project=&status=&query=&sort=&cursor=&limit=&daily=&environment=` | issues, con `user_count`, los usuarios distintos afectados; `query` va en el [lenguaje de búsqueda](#búsqueda); `status` es `open`, `resolved`, `resolved_next`, `silenced` o `all`, y vacío es `open` salvo que `query` traiga `is:`; `sort`, `last_seen` (por defecto), `first_seen` o `count`; `project`, id o nombre; con `daily=1`, cada issue lleva `daily`, sus eventos por día de los últimos 14 días como en el detalle; con `environment`, solo los issues con eventos en ese entorno, y sus eventos, primera y última vez y `daily` son los de ese entorno (`user_count` sigue siendo el de todos) |
| `GET /api/0/issues/{id}/?environment=` | el issue con su último evento, los eventos por día de los últimos 14 días, los valores más frecuentes de entorno, release, usuario y `organization_id`, y su `first_release`, `last_release` y `fixed_in_release`; con `environment`, todo de ese entorno |
| `GET /api/0/issues/{id}/events/?environment=` | sus eventos, del más nuevo al más viejo |
| `GET /api/0/issues/{id}/tags/` | `user_count` y, de cada etiqueta, el total de eventos que la traían, cuántos valores distintos tiene y los 10 más frecuentes con su recuento, su porcentaje y su última vez |
| `GET /api/0/tags/?project=` | las claves de etiqueta que hay, con en cuántos issues sale cada una; para autocompletar |
| `GET /api/0/tags/{key}/values/?prefix=&project=` | los 20 valores más frecuentes de una clave que empiezan por `prefix`, sin distinguir mayúsculas |
| `GET /api/0/issues/{id}/stacktrace.md` | el último evento en Markdown, pensado para un modelo |
| `POST /api/0/issues/{id}/status` | cambia el estado con `{"status": "open\|resolved\|resolved_next\|silenced"}`; `resolved_next` guarda el release del último evento (409 si no tiene) |
| `GET /api/0/environments/?project=` y `GET /api/0/projects/{id}/environments/` | los entornos de los eventos, con cuántos issues y eventos tienen y cuándo se vio el último |
| `GET /api/0/releases/?project=&environment=&cursor=&limit=` y `GET /api/0/projects/{id}/releases/` | las releases, de la más nueva a la más vieja, con sus despliegues y cuántos errores nuevos (`new_issues`), con eventos (`issues`), regresiones y eventos trajo cada una; con `environment`, las que tienen eventos o despliegues en él, contando solo lo suyo |
| `POST /api/0/projects/{id}/releases/` | crea la release `{"version": "..."}` (201, o 200 si ya existía); con `"environment"` (y `"deployed_at"`, opcional) apunta además un despliegue y devuelve en `resolved_issues` los issues que resolvió |
| `GET /api/0/projects/{id}/releases/{version}/?environment=` | la release con sus despliegues, la anterior y la siguiente, y sus issues: `new` (aparecieron con ella), `regressed`, `continued` (ya existían y siguieron saliendo) y `stopped` (salían en la anterior y en esta no, o se resolvieron al desplegarla) |
| `GET` y `POST /api/0/projects/{id}/releases/{version}/deploys/` | sus despliegues; `POST` con `{"environment": "production"}` apunta uno (y crea la release si no existía) |
| `GET /api/0/events/{id}/` | el evento normalizado entero, por id o por `event_id` |
| `GET /api/0/projects/{id}/filters` | los filtros, el tope por issue, el muestreo y el límite por minuto del proyecto |
| `PATCH /api/0/projects/{id}/filters` | cambia los campos que trae el cuerpo, con los mismos nombres; `null` en un número vuelve a su valor por defecto; 400 con un valor no válido |
| `GET /api/0/projects/{id}/dropped?days=` | lo descartado en los últimos `days` días (30 por defecto, hoy incluido): `total`, `reasons` con cada motivo y `daily` por día y motivo |
| `GET`, `POST /api/0/projects/{id}/rules` | sus reglas de aviso, con los secretos tapados; crea una (201) |
| `GET`, `PUT`, `DELETE /api/0/projects/{id}/rules/{rule}` | una regla; la sustituye entera; la borra (204) |
| `POST /api/0/projects/{id}/rules/{rule}/test` | con `{"action_id": "..."}`, manda un aviso de prueba por esa acción; 502 con el error si el destino falla |

Las listas devuelven `{"data": [...], "next_cursor": "..."}`: para la página siguiente, se pasa `cursor=<next_cursor>`; `limit` va de 1 a 100 (25 por defecto). Los errores son `{"error": "..."}`, con 401 si falta el token, no existe o está revocado.

### Búsqueda

La `query` de la lista (en la API, en `list_issues` del MCP y en la barra de la interfaz) son términos separados por espacios que se cumplen todos a la vez:

| Término | Qué busca |
|---|---|
| `is:open`, `is:resolved`, `is:resolved_next`, `is:silenced` | el estado |
| `is:regressed` | los abiertos que volvieron tras resolverse |
| `release:1.4.0`, `environment:production`, `level:warning` | issues con algún evento de ese release, entorno o nivel |
| `project:web` | el proyecto, por nombre o id |
| `user:ana@example.com` | issues que le han pasado a ese usuario, por id, email (sin distinguir mayúsculas), username o IP; hace falta el valor exacto, porque los usuarios se guardan con hash |
| `browser:Safari`, `os:"Mac OS"`, `device:iPhone`, `runtime:CPython`, `component:api` | cualquier etiqueta, con su valor exacto: las que salen de `contexts` y del user agent (`browser`, `os`, `device`, `runtime`) y las `tags` del SDK |
| `!browser:Chrome`, `!is:resolved`, `!timeout` | `!` delante niega: issues sin ningún evento con ese valor, o sin ese texto |
| `timeout`, `"division by zero"` | texto en el título o el culprit, sin distinguir mayúsculas; las comillas agrupan espacios (`\"` y `\\` dentro) |

Ejemplos:

```text
is:open browser:Safari                    # abiertos que pasan en Safari
is:regressed release:1.4.0                # reabiertos que han vuelto a pasar en el 1.4.0
project:web os:"Mac OS" !environment:staging
user:ana@example.com is:open              # lo que le está fallando a una persona
"Cannot read properties" level:error
```

Una clave es una letra o `_` seguida de letras, números, `_`, `.` o `-`; lo que no tiene esa forma antes de los dos puntos (una URL, `12:30`) o no lleva nada detrás de ellos es texto, así que un título pegado tal cual, como `TypeError: Cannot read properties`, busca ese texto. Una búsqueda mal escrita da 400 con `query_error`: `code` (`unclosed_quote`, `empty_key`, `lone_negation`, `bad_status` o `after_quote`), `position` (el carácter, desde 0) y, si viene al caso, `key` y `value`; el mensaje de `error` dice lo mismo en español.


## Ingesta

Los SDK envían a `POST /api/<id>/envelope/` con la clave del DSN en `X-Sentry-Auth` o en `sentry_key` de la query. Se aceptan hasta 1 MiB comprimido y 10 MiB descomprimido. Solo se guardan los items `event`; el resto se acepta y se ignora. Pasado el límite por minuto del proyecto (`RATE_LIMIT_PER_MINUTE` si no pone el suyo), responde 429 con `Retry-After` y `X-Sentry-Rate-Limits`; el límite se cuenta en memoria, por réplica.

Cada evento se agrupa en un issue por su huella: el `fingerprint` del SDK si lo trae; si no, el tipo de la excepción lanzada y el módulo o fichero y la función de sus fotogramas `in_app` (sin líneas); y en los logs sin excepción, el logger y el mensaje con UUIDs, hexadecimales y números cambiados por marcadores. Un evento en un issue `resolved`, o en uno `resolved_next` con una release posterior, lo reabre como regresión ([Releases, despliegues y entornos](#releases-despliegues-y-entornos)).

Cada issue cuenta, con cada evento, los valores de sus etiquetas y su última vez: release, entorno, nivel, `browser`, `os` y `device` (de `contexts` o, si no vienen, del user agent), `runtime` y hasta 50 `tags` del SDK, con las claves de hasta 32 caracteres y los valores cortados a 200. También cuenta los usuarios distintos, por id, email, username o IP, en este orden: guarda solo los 16 primeros bytes de un sha256 con una clave aleatoria de la instalación (tabla `user_hash_key`): el recuento no guarda el dato en claro, aunque el evento guardado lo sigue llevando, como hasta ahora, hasta que la retención lo borra. Un evento repetido no cuenta dos veces; los que pasan del tope o quedan fuera de la muestra sí cuentan, igual que en el número de eventos.

Cada hora se borran los eventos de más de `RETENTION_DAYS` días y los issues que se quedan sin eventos.

### Filtros, tope y muestreo

Cada proyecto tiene su configuración, con `projects filters` o con `/api/0/projects/{id}/filters`. Por defecto no filtra nada, guarda como mucho 100 eventos por issue y hora, no muestrea y usa `RATE_LIMIT_PER_MINUTE`.

Los filtros se aplican al recibir el evento, antes de agruparlo: lo filtrado no abre ni cuenta en ningún issue, no avisa y no gasta el límite por minuto.

| Clave | Descarta | Motivo en las cifras |
|---|---|---|
| `messages` | los eventos cuyo mensaje, o el `Tipo: valor` o el valor de alguna excepción, casa con alguna de estas expresiones regulares (sintaxis de Go; `(?i)` para no distinguir mayúsculas) | `message` |
| `exception_types` | los que tienen alguna excepción de estos tipos, por su nombre (`KeyError`) o con el módulo delante (`pagos.errores.ReembolsoRechazado`) | `exception_type` |
| `browser_extensions` (`on`/`off`) | los errores de extensiones del navegador: `chrome-extension://`, `moz-extension://`, `safari-extension://`, `safari-web-extension://`, `ms-browser-extension://` o `edge-extension://` en un fotograma, el mensaje o una excepción | `browser_extension` |
| `web_crawlers` (`on`/`off`) | los de peticiones de bots, rastreadores y comprobadores de disponibilidad, por el `User-Agent` que trae el evento en `request.headers` | `web_crawler` |
| `localhost` (`on`/`off`) | los de `request.url` en `localhost`, `*.localhost`, `127.0.0.0/8`, `::1` o `0.0.0.0`, o con la IP del usuario en una de esas direcciones | `localhost` |
| `environments` | los de estos entornos | `environment` |
| `releases` | los de estos releases; `*` vale cualquier cosa (`*-beta.*`) | `release` |

El resto limita lo que se guarda sin perder la cuenta:

- `events_per_issue_per_hour` (100 por defecto): pasado el tope en una hora de reloj, el evento no se guarda, pero el issue suma su `count` y su última vez, así que la cifra es la real. No vuelve a avisar: los avisos siguen siendo solo los de error nuevo y regresión. Motivo `issue_limit`.
- `sample_rate`, de 0 a 1 (1 por defecto): la parte de los eventos de issues que ya existen que se guarda; los que quedan fuera cuentan en el issue igual que los del tope. Motivo `sampled`.
- `rate_limit_per_minute`: el límite por minuto del proyecto, en lugar de `RATE_LIMIT_PER_MINUTE`. Lo que lo pasa se rechaza con 429 y no cuenta en ningún issue. Motivo `rate_limit`.

El primer evento de un issue nuevo y el de una regresión se guardan siempre, aunque el tope o el muestreo digan lo contrario. El muestreo también deja pasar el primer evento de cada día (UTC) de cada issue, para que un issue que sigue recibiendo eventos tenga siempre uno guardado dentro de `RETENTION_DAYS` y la purga no lo borre con su `count`. Si un evento que no se guardó se vuelve a enviar con el mismo `event_id`, cuenta otra vez, porque no queda nada con que reconocerlo; los SDK solo reenvían si la respuesta no es un 200.

Las cifras de lo descartado se cuentan en memoria y se guardan cada 10 segundos y al parar, por proyecto, motivo y día (UTC); si el proceso muere sin parar, se pierden las de esos últimos segundos.

Los tests de `internal/store`, `internal/ingest`, `internal/sourcemap`, `internal/api`, `internal/alert` y de los subcomandos usan Postgres solo si existe `INTAKE_ERRORS_TEST_DATABASE_URL`; cada test crea su propio esquema y lo borra al acabar. Con el Postgres de `docker compose`:

```sh
INTAKE_ERRORS_TEST_DATABASE_URL='postgres://intake_errors:intake_errors@127.0.0.1:5433/intake_errors?sslmode=disable' go test ./...
```

## Releases, despliegues y entornos

Cada proyecto tiene sus releases: la versión, cuándo se vio por primera vez y sus despliegues (entorno y fecha). Una release se crea sola con el primer evento que trae una versión nueva, y también a mano:

```sh
intake-errors releases new web web@1.4.0                     # la crea
intake-errors releases new web web@1.4.0 --env production    # y apunta un despliegue en production
```

o con la API (`POST /api/0/projects/{id}/releases/` y `.../releases/{version}/deploys/`, en [API y MCP](#api-y-mcp)) o con `sentry-cli` ([abajo](#releases-con-sentry-cli)). Una release es posterior a otra si se vio después.

De cada release se sabe qué trajo: los errores que aparecieron con ella, los que ya existían y siguieron saliendo, las regresiones que llegaron con ella y los que dejaron de salir. Se cuentan por issue, release y entorno al recibir cada evento, también los que no se guardan por el tope o el muestreo, así que no dependen de la retención de los eventos. Un issue recuerda la release de su primer evento.

**Resuelto en la siguiente versión.** Un issue en `resolved_next` guarda la release de su último evento. Cuando se despliega una release posterior (en cualquier entorno), pasa a `resolved` y queda apuntada como la release que lo resolvió (`fixed_in_release`). A partir de ahí, un evento de esa release o de una posterior es una regresión: se reabre y avisa; uno de una release anterior (un cliente que aún no ha actualizado) o sin release no lo reabre. Mientras sigue en `resolved_next`, solo lo reabre un evento de una release posterior a la que tenía.

Qué release es posterior se decide por su número cuando las dos se leen como versiones del mismo paquete (`1.4.0`, `v2.0.0-rc.1`, `web@2026.11.21`, como en semver: con dos números o más, la pre-release antes que la versión y sin contar el `+build`). Así, una versión vieja que nunca había mandado nada no reabre ni resuelve nada, y una vuelta atrás tampoco. Si alguna no se lee como versión (un hash de git, `build-a1`…), se decide por cuándo se vio por primera vez, y entonces el evento que estrena una release no cuenta como posterior, porque puede ser de un cliente viejo; el siguiente de esa release ya sí, y una creada a mano o desplegada cuenta desde el primero. Con esos nombres, una vuelta atrás a uno que nunca se había visto cuenta como posterior al desplegarlo y resuelve los issues en `resolved_next`; se evita creando cada release al construirla, antes de desplegar las siguientes.

**Entornos.** La lista de entornos de cada proyecto sale de los eventos. En la interfaz, el selector de entorno del raíl filtra la lista de errores, el detalle con su gráfica y sus recuentos, y las releases; por defecto, todos los entornos, y la elección se guarda en el navegador. En la API y el MCP es el parámetro `environment`.

### Releases con sentry-cli

Con las mismas variables que para los source maps ([Con sentry-cli](#con-sentry-cli)):

```sh
sentry-cli releases new "$SENTRY_RELEASE"         # la crea en SENTRY_PROJECT (o en los de -p)
sentry-cli releases finalize "$SENTRY_RELEASE"    # la marca como terminada
sentry-cli deploys new -r "$SENTRY_RELEASE" -e production
```

| Ruta | Qué hace |
|---|---|
| `POST /api/0/organizations/{org}/releases/` | crea la release en cada proyecto de `projects`; si ya existía, la deja como estaba |
| `PUT /api/0/organizations/{org}/releases/{version}/` | la finaliza en `dateReleased` (o ahora) en los proyectos de `projects` o, sin ellos, en todos los que la tienen |
| `POST /api/0/organizations/{org}/releases/{version}/deploys/` | apunta el despliegue en `environment` en todos los proyectos que tienen la release (404 si ninguno), en la fecha de `dateFinished` o ahora; resuelve los issues en `resolved_next` de una release anterior |

`tools/sentry-cli/prueba.sh` los prueba con `sentry-cli` 3.8.2 de verdad contra el binario, y `testdata/sentry-cli/releases/` es lo que mandó.

## Source maps del front

Los errores del navegador se traducen a su TypeScript original con los `.map` del build. Se guardan comprimidos por proyecto, release, dist y nombre del `.js` (los chunks de un build de producción llevan un hash en el nombre) y, si el build pasó por `sentry-cli sourcemaps inject`, también por su debug ID. Se suben con el endpoint de abajo o con `sentry-cli` ([Con sentry-cli](#con-sentry-cli)), y se borran a los `RETENTION_DAYS` días de subirlos.

`POST /api/{project_id}/releases/{release}/sourcemaps/` recibe un multipart con uno o varios campos `file` (`chunk-XXXX.js.map`) y `Authorization: Bearer <token>`, con `SOURCEMAP_UPLOAD_TOKEN` o un token de la API. Volver a subir un fichero lo sustituye. Responde con lo guardado:

```json
{"release": "2026.11.19-1", "files": [{"name": "chunk-Z554BZQ6.js.map", "file": "chunk-Z554BZQ6.js", "size": 6177}]}
```

Errores, como `{"error": "..."}`: 401 sin token o con uno malo, 404 si el proyecto no existe, 413 si un fichero pasa de 20 MiB o la petición de 100 MiB, y 400 si un fichero no es un source map v3 válido (con su nombre). Si algo falla no se guarda nada de esa petición.

Cómo subirlos en el build, en [Desplegar](#desplegar).

Al recibir un evento, cada fotograma de un `.js` con mapa pasa a su fichero original (`src/...`), con su línea, columna y 5 líneas de contexto antes y después de `sourcesContent`. El mapa se busca primero por el debug ID que el SDK manda en `debug_meta.images` (las imágenes `sourcemap`, por la URL del `.js`); si el evento no lo trae o ese debug ID no está subido, por el `release` y el `dist` del evento y el nombre del `.js` (un mapa subido sin dist vale para cualquiera). El evento guardado conserva `debug_meta` y `dist`. `in_app` queda falso para lo que viene de `node_modules/`. La función se deja como la manda el SDK, y la posición minificada se guarda en `minified`. Sin debug ID ni release, sin mapa o con un mapa roto, el fotograma se queda como estaba.

La ingesta pasa cada evento por esta traducción antes de agruparlo, así que los errores del navegador se agrupan por su fichero y función originales.

### Con sentry-cli

`sentry-cli` sube a intake-errors igual que a Sentry; solo cambian la URL y el token:

```sh
export SENTRY_URL=https://errors.example.com   # la URL del servicio, sin ruta
export SENTRY_AUTH_TOKEN=...                    # SOURCEMAP_UPLOAD_TOKEN o un token de la API (ie_…)
export SENTRY_ORG=mi-org                        # se acepta cualquiera
export SENTRY_PROJECT=web                       # el nombre del proyecto en intake-errors
```

Sin `inject`, los mapas se buscan por release y nombre, así que `--release` tiene que ser el `release` que manda el SDK:

```sh
sentry-cli sourcemaps upload --release "$SENTRY_RELEASE" dist/
```

Con `inject`, cada `.js` y su mapa llevan un debug ID, el SDK lo manda con cada error y el mapa se encuentra por él, coincida o no el release. Hay que desplegar los `.js` ya inyectados:

```sh
sentry-cli sourcemaps inject dist/
sentry-cli sourcemaps upload dist/   # --release y --dist son opcionales
```

Las versiones actuales (probado con 3.8.2) suben un artifact bundle por trozos:

| Ruta | Qué hace |
|---|---|
| `GET /api/0/organizations/{org}/chunk-upload/` | las opciones de la subida: trozos de 8 MiB en gzip con su sha1, hasta 32 MiB por petición y 100 MiB por bundle |
| `POST /api/0/organizations/{org}/chunk-upload/` | guarda los trozos (`file_gzip` o `file`, con el sha1 del trozo de nombre); si el sha1 no coincide, 400 |
| `POST /api/0/organizations/{org}/artifactbundle/assemble/` | `not_found` con los trozos que faltan; `ok` cuando el bundle queda guardado, o si ya lo estaba y sus trozos caducaron; `error` con el motivo si el bundle no es un zip, no trae `manifest.json`, un mapa no es un source map v3 o sus mapas pasan de 100 MiB descomprimidos, y entonces no se guarda nada de él |

Del bundle se guardan solo los `.map`, con el release y el dist de la petición (o, si no los trae, los de su `manifest.json`), la URL de su `.js` (`~/assets/main-XXXX.js`) y su debug ID. Los trozos se guardan un día, aunque ya se hayan ensamblado, porque otra subida del mismo build puede estar usándolos; mientras están, volver a subir un bundle vuelve a guardar sus mapas (los repone si otra subida los sustituyó por nombre) y renueva su fecha para la retención.

Las versiones antiguas (probado con 1.77.3, `sentry-cli releases files <release> upload-sourcemaps dist/`) usan `POST /api/0/projects/{org}/{project}/releases/`, que crea la release, y suben cada fichero a `POST /api/0/projects/{org}/{project}/releases/{release}/files/`: se guardan los `.map` y el resto se acepta y se descarta.

La organización se acepta tal cual y el proyecto se busca por nombre (404 si no existe). Con un token que no vale, 401.

## Fixtures

`testdata/envelopes/` son envíos grabados de sentry-python 2.75.0 y `@sentry/browser` 11.15.0 desde dos apps de ejemplo (una de FastAPI y una página TypeScript compilada con esbuild), `testdata/sourcemaps/` el build de la página con sus mapas y `testdata/sentry-cli/` las subidas de ese build con sentry-cli 3.8.2. `tools/sentry-cli/prueba.sh` es la prueba de punta a punta con `sentry-cli` de verdad. Las apps, cómo regrabarlos y qué debe pasar con cada envío están en [`tools/fixtures/README.md`](tools/fixtures/README.md); `internal/event/fixtures_test.go` sigue esa tabla.
