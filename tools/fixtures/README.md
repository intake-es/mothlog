# Envíos grabados de los SDK

Grabados el 2026-11-19 con dos apps de ejemplo de este directorio, sin tocar los SDK:

- `python/`: una app de FastAPI con `sentry-sdk` 2.75.0 y `structlog`.
- `navegador/`: una página TypeScript con `@sentry/browser` 11.15.0 y `mitt`, compilada con esbuild en dos ficheros con hash y sus source maps.

Cada envío son dos ficheros:

- `NNN.envelope`: el cuerpo tal cual llegó (los de Python, en gzip).
- `NNN.meta.json`: método, ruta, query y cabeceras (`Content-Encoding`, `X-Sentry-Auth` o `sentry_key` en la query).

## testdata/envelopes/

| Fichero | SDK | Qué es | Qué debe pasar |
|---|---|---|---|
| `api-excepcion-1` | sentry-python 2.75.0 | `ZeroDivisionError` sin capturar en `GET /pedidos/{pedido_id}/media` | Error nuevo; el fotograma de `app.py` es `in_app` y trae su código |
| `api-validacion-1` | 2.75.0 | `ValidationError` de pydantic en `GET /pedidos/importar` | Error nuevo |
| `api-excepcion-2`, `api-validacion-2` | 2.75.0 | `sessions` que acompañan a cada error | Se aceptan y se ignoran |
| `api-descartado-1` y `-2` | 2.75.0 | `NotFound` que descarta `before_send`: solo llegan `sessions` y `client_report` | Nada |
| `api-log-exception-1` | 2.75.0 | `log.exception("stock.sincronizacion_fallida", ...)` de structlog con un `KeyError` | Error nuevo |
| `api-log-error-1` | 2.75.0 | `log.error("pagos.reembolso_rechazado", ...)` de structlog, sin excepción | Error nuevo, con el nombre del evento de título |
| `api-tarea-a-1` y `-b-1` | 2.75.0 | `task failed id=… name='enviar_factura' worker=… try=4 duration_ms=…` del logger `worker`, con id, worker y duración distintos | Los dos, el mismo error |
| `web-typeerror-1` | `@sentry/browser` 11.15.0 | `TypeError` en `src/carrito.ts` al pulsar un botón, a través de `mitt`; lo captura el `addEventListener` envuelto del SDK; release `fixture-release-1` | Error nuevo; con los mapas de `testdata/sourcemaps/`, el último fotograma sale como `src/carrito.ts:7` y los de `mitt`, con `in_app` falso |
| `web-sesion-1` y `-2` | 11.15.0 | `session` del navegador: la del arranque y la que cuenta el error | Se aceptan y se ignoran |

Proyectos de los DSN: `api` es el `1` y `web` el `2`. La clave pública va en `X-Sentry-Auth` (Python) o en `sentry_key` de la query (navegador).

La app de Python pone `server_name`, `release`, `environment` y una etiqueta `component` fijos, y en `before_send` cambia las rutas de la máquina que graba por `/srv/api` y `/usr/lib/python3.14`, para que no salgan en los fixtures.

## testdata/sourcemaps/

- `main-3TO43VUM.js` y su `.map`: la entrada, con `@sentry/browser` y `src/main.ts`.
- `chunk-Z554BZQ6.js` y su `.map`: `src/carrito.ts`, `src/bus.ts` y `mitt`, cargados con un `import()`.

Los mapas traen `sourcesContent`. esbuild escribe las fuentes relativas a `dist/` (`../src/carrito.ts`); `build.mjs` les quita el `../` para que queden como en un build de Angular. Con `source-map` 0.8.0 (`node posiciones.mjs`), los fotogramas de `web-typeerror-1` son:

| Fotograma | Original |
|---|---|
| `main-3TO43VUM.js:15:14711` | `node_modules/@sentry/browser/src/helpers.ts:157` |
| `main-3TO43VUM.js:15:31889` | `src/main.ts:11` |
| `chunk-Z554BZQ6.js:1:492` | `src/carrito.ts:19` |
| `chunk-Z554BZQ6.js:1:226` | `node_modules/mitt/src/index.ts:108` |
| `<anonymous>` (`Array.map`) | sin posición |
| `chunk-Z554BZQ6.js:1:242` | `node_modules/mitt/src/index.ts:109` |
| `chunk-Z554BZQ6.js:1:442` | `src/carrito.ts:12` |
| `chunk-Z554BZQ6.js:1:383` | `src/carrito.ts:7`, donde salta el error |

## Regrabar

1. `python3 tools/fixtures/grabador.py <salida>` deja escuchando el receptor en `127.0.0.1:8299`; guarda cada envío como `NNN.envelope` y `NNN.meta.json`.
2. Python, con [uv](https://docs.astral.sh/uv/):

   ```sh
   cd tools/fixtures/python
   for caso in excepcion validacion descartado log-exception log-error tarea tarea; do
     SENTRY_DSN=http://<clave>@127.0.0.1:8299/1 uv run grabar.py $caso
   done
   ```

3. Navegador, con Playwright y su Chromium (`npx playwright install chromium`):

   ```sh
   cd tools/fixtures/navegador
   npm ci
   node build.mjs https://<clave>@errors.example.com/2   # deja el build en dist/
   node grabar.mjs <salida>
   ```

   `grabar.mjs` sirve `dist/` como `https://app.example.com`, pulsa el botón, guarda en `<salida>` lo que iba a `errors.example.com`, con la misma numeración que el receptor, y bloquea todo lo demás.
4. `python3 tools/fixtures/ver.py <salida>` lista los items de cada envío. Renombrar los ficheros como en la tabla y copiarlos a `testdata/envelopes/`, y copiar `dist/*.js` y `dist/*.map` a `testdata/sourcemaps/`. `node posiciones.mjs <envío> dist` da las posiciones originales de cada fotograma. Si cambian las claves, los ids, los hashes de los chunks o las líneas, ajustar los tests que los esperan.
