# Sending errors

Mothlog receives what the Sentry SDKs send. Use the official SDK of your language and give it the DSN that `mothlog projects create <name>` prints ([Command line](cli.md#projects)): `https://<key>@<host>/<project id>`. Nothing else changes. The project's fixtures are recorded with `sentry-python` 2.75.0 and `@sentry/browser` 11.15.0.

What Mothlog keeps from the envelope is in [Ingestion](ingestion.md): errors and logged exceptions, sessions and cron check-ins. Transactions, spans, profiles, replays, attachments and client reports are accepted and ignored, so performance monitoring can stay on in the SDK without errors, but it shows nothing.

## Python

```sh
pip install sentry-sdk
```

```python
import sentry_sdk

sentry_sdk.init(
    dsn="https://<key>@errors.example.com/1",
    release="api@1.4.0",
    environment="production",
)
```

Unhandled exceptions are sent by themselves, and with the web framework integrations (FastAPI, Django, Flask…) so are the errors of each request. To send one by hand, `sentry_sdk.capture_exception(e)` or `sentry_sdk.capture_message("…")`. The SDK sends what is pending when the program exits; `sentry_sdk.flush()` does it at any point.

Log records: `sentry-sdk` 2.x sends `logging.error(…)` and `logger.exception(…)` as events by default; from 3.0 on, only if you ask for it:

```python
import logging

from sentry_sdk.integrations.logging import LoggingIntegration

sentry_sdk.init(dsn="…", integrations=[LoggingIntegration(event_level=logging.ERROR)])
```

## JavaScript in the browser

```sh
npm install @sentry/browser
```

```js
import * as Sentry from '@sentry/browser';

Sentry.init({
  dsn: 'https://<key>@errors.example.com/2',
  release: 'web@1.4.0',
  environment: 'production',
});
```

Uncaught errors and unhandled promise rejections are sent by themselves; `Sentry.captureException(e)` sends one by hand. The same goes for the framework SDKs (`@sentry/react`, `@sentry/angular`…) and for `@sentry/node` on the server. The ingestion endpoint answers CORS, so a page on any origin can send to it. To see the original TypeScript instead of the minified code, [upload the source maps](#source-maps).

## Release health

The SDKs send sessions: one per page load in the browser and, in the server SDKs, one per request, already added up per minute. With them, each release shows how many of its sessions and users had no crash, in the release list and detail, in the API (`health`) and in the MCP (`list_releases`, `get_release`):

- **Crash-free sessions**: 1 − sessions with a crash / sessions.
- **Crash-free users**: 1 − users with a crashed session / users.
- **Adoption**: the share of the project's sessions of the last 24 hours that belong to that release.

They are computed per release and environment (`health_by_environment`) and per release across environments (`health`, or the one in the `environment` parameter). Without sessions there is no percentage: the API returns `null` and the UI says "No sessions"; without `did`, the same for users.

What to turn on in the SDKs:

- **Browser** (`@sentry/browser` and the framework SDKs): it is on through `browserSessionIntegration`, one of the default integrations. Only `release` in `Sentry.init` is needed; if you drop the default integrations, add it back.
- **Python** (`sentry-sdk`): `auto_session_tracking` is on and, with the web integrations, sends the sessions of requests added up in `sessions` items. For a command-line app or a worker, call `sentry_sdk.start_session()` and `sentry_sdk.end_session()` around its life.
- In all of them: `release` (without a release nothing is counted) and, to tell them apart, `environment`. Users come from the SDK's user (`Sentry.setUser({id})`, `sentry_sdk.set_user({"id": ...})`), which the SDK sends as `did`.

How they are counted:

- They are kept added up by hour, project, release and environment: total, errored, crashed and abnormal. A session counts as crashed if it ends `crashed` or `unhandled`, which is how the browser SDK marks an uncaught error.
- A single session arrives several times (the start with `init`, the updates and the end) and counts once, with its last state: while it can still arrive, Mothlog remembers what it already counted by its `sid` (hashed), and forgets it after 24 hours without news. A resend or an older update changes nothing, and neither does anything after the end; an update without its `init` doesn't count until the `init` arrives.
- `sessions` items from server SDKs carry no id, so a resend of the same item counts twice; SDKs only resend when the answer isn't a 200.
- Distinct users are kept per release and environment with the hashed `did`, with the same key as issue users; without `did`, the session only counts in sessions.
- The project's `environments` and `releases` filters drop their sessions too.
- Sessions have their own per-minute limit, 10 times the project's event limit, because a browser sends one or two per page view; each `session` item counts one and each `sessions` item one per group, since each group can bring a different user. An envelope with only sessions that doesn't fit gets a 429 with the `session` category in `X-Sentry-Rate-Limits`; if it brings events that do fit, they are saved and the answer is a 200 with that header. If an envelope gets a 429 for its events, its sessions aren't counted either. What doesn't fit is counted as dropped with the reason `session_limit`.
- A `sessions` item with more than 1,000 groups isn't valid and nothing in it counts (reason `session_invalid`).
- A session only creates its release if the project has fewer than 10,000; otherwise it is dropped (reason `session_releases`). Events and the API create releases without that cap.
- Retention is the events': every hour, session hours and users older than `RETENTION_DAYS` days are deleted, and so are unfinalized releases first seen more than `RETENTION_DAYS` days ago that nothing uses any more: no events, issue counts, sessions, deploys, source maps or bundles, and no issue that has it as its first release, its `resolved_next` release or the one that resolved it.

## Releases, deploys and environments

Each project has its releases: the version, when it was first seen, and its deploys (environment and date). A release is created by the first event that brings a new version, and also by hand:

```sh
mothlog releases new web web@1.4.0                     # creates it
mothlog releases new web web@1.4.0 --env production    # and records a deploy to production
```

or with the API (`POST /api/0/projects/{id}/releases/` and `…/releases/{version}/deploys/`, in [API](api.md)) or with [`sentry-cli`](#releases-with-sentry-cli).

Each release knows what it brought: the errors that appeared with it, the ones that already existed and kept happening, the regressions that came with it, and the ones that stopped. They are counted per issue, release and environment as each event arrives, including the ones not kept because of the cap or the sampling, so they don't depend on event retention. An issue remembers the release of its first event.

**Resolved in the next release.** An issue in `resolved_next` keeps the release of the last event to arrive, not the one with the highest `timestamp`, which the SDK sets. When a later release is deployed (to any environment), it becomes `resolved` and that release is recorded as the one that resolved it (`fixed_in_release`). From then on, an event from that release or a later one is a regression: the issue reopens and alerts; one from an earlier release (a client that hasn't updated yet) or without a release doesn't reopen it. While it is still in `resolved_next`, only an event from a release later than the one it had reopens it.

Which release is later is decided by its number when both read as versions of the same package (`1.4.0`, `v2.0.0-rc.1`, `web@2026.11.21`), as in semver: with two numbers or more, a pre-release before the release and ignoring `+build`, so `web@1.1.0+build.2` is `web@1.1.0`; `git describe` names like `v1.2.0-5-gabc123` go after their tag and among themselves by the number of commits. So an old version that had never sent anything neither reopens nor resolves anything, and neither does a rollback. If either doesn't read as a version (a git hash, `build-a1`…), it is decided by when each was first seen, and then the event that first brings a release doesn't count as later, because it may come from an old client; the next one from that release does, and a release created by hand or deployed counts from the start. With such names, a rollback to one never seen before counts as later when deployed and resolves the issues in `resolved_next`; create each release when it is built, before deploying later ones, to avoid it.

**Environments.** Each project's list of environments comes from its events. In the UI, the environment picker in the sidebar filters the issue list, the issue detail with its chart and counts, and the releases; it starts with all environments, and the choice is kept in the browser. In the API and the MCP it is the `environment` parameter.

## Source maps

Browser errors are translated to their original TypeScript with the build's `.map` files. They are stored compressed per project, release, dist and the name of the `.js` (production chunks carry a hash in their name) and, if the build went through `sentry-cli sourcemaps inject`, by their debug ID too. They are uploaded with `sentry-cli` or with the endpoint below, and deleted `RETENTION_DAYS` days after upload.

When an event arrives, each frame of a `.js` with a map is moved to its original file (`src/...`), line and column, with 5 lines of context before and after from `sourcesContent`. The map is looked up first by the debug ID the SDK sends in `debug_meta.images`; if the event doesn't bring it or that debug ID wasn't uploaded, by the event's `release` and `dist` and the name of the `.js` (a map uploaded without a dist works for any). The saved event keeps `debug_meta` and `dist`. `in_app` is false for what comes from `node_modules/`. The function name stays as the SDK sent it, and the minified position is kept in `minified`. Without a debug ID or release, without a map or with a broken one, the frame stays as it was. The file a frame shows is the map's `sources` entry (with its `sourceRoot`, and without `webpack://` or a leading `./`): bundlers that write it relative to the map, as esbuild does, show paths like `../../src/cart.ts`. Since this happens before grouping, browser errors are grouped by their original file and function.

What an event costs is bounded however many frames it has: at most 250 frames are translated, the innermost first; each map is read once per event; each context line is cut to 512 characters and, once the event carries 256 KiB of context, the remaining frames are translated without their lines; and no map is looked up with a `release`, `dist` or `.js` name longer than 512 characters. Maps are kept in a 64 MiB in-memory cache, which counts what each parsed map takes and each lookup that found nothing, and each map expires from it after an hour; retention empties it on every replica.

### With sentry-cli

`sentry-cli` uploads to Mothlog as it does to Sentry; only the URL and the token change. The token is `SOURCEMAP_UPLOAD_TOKEN`, if the server has one, or an API token: `mothlog tokens create ci-uploads` (with Docker Compose, `docker compose exec mothlog /mothlog tokens create ci-uploads`).

```sh
export SENTRY_URL=https://errors.example.com   # the service's URL, without a path
export SENTRY_AUTH_TOKEN=...                    # SOURCEMAP_UPLOAD_TOKEN or an API token (ml_…)
export SENTRY_ORG=my-org                        # any value is accepted
export SENTRY_PROJECT=web                       # the project's name in Mothlog
```

Without `inject`, maps are looked up by release and name, so `--release` has to be the `release` the SDK sends:

```sh
sentry-cli sourcemaps upload --release "$SENTRY_RELEASE" dist/
```

With `inject`, each `.js` and its map get a debug ID, the SDK sends it with each error and the map is found by it, whether the release matches or not. Deploy the injected `.js` files:

```sh
sentry-cli sourcemaps inject dist/
sentry-cli sourcemaps upload dist/   # --release and --dist are optional
```

Current versions (tested with 3.8.2) upload an artifact bundle in chunks:

| Route | What it does |
|---|---|
| `GET /api/0/organizations/{org}/chunk-upload/` | the upload options: 8 MiB gzip chunks with their SHA-1, up to 32 MiB per request and 100 MiB per bundle |
| `POST /api/0/organizations/{org}/chunk-upload/` | stores the chunks (`file_gzip` or `file`, named by the chunk's SHA-1); 400 if the SHA-1 doesn't match |
| `POST /api/0/organizations/{org}/artifactbundle/assemble/` | `not_found` with the missing chunks; `ok` once the bundle is stored, or if it already was and its chunks expired; `error` with the reason if the bundle isn't a zip, has no `manifest.json`, a map isn't a v3 source map or doesn't fit in the source map cache once parsed, or its maps are over 100 MiB uncompressed, and then nothing of it is stored |

Only the `.map` files of the bundle are kept, with the release and dist of the request (or, without them, those of its `manifest.json`), the URL of their `.js` (`~/assets/main-XXXX.js`) and their debug ID. Chunks are kept for a day, even once assembled, because another upload of the same build may be using them; while they are, uploading a bundle again stores its maps again and renews their date for retention.

Old versions (tested with 1.77.3, `sentry-cli releases files <release> upload-sourcemaps dist/`) use `POST /api/0/projects/{org}/{project}/releases/`, which creates the release, and upload each file to `POST /api/0/projects/{org}/{project}/releases/{release}/files/`: `.map` files are kept and the rest is accepted and dropped.

The organization is accepted as it comes and the project is looked up by name (404 if it doesn't exist). With a token that doesn't work, 401.

### With curl

`POST /api/{project_id}/releases/{release}/sourcemaps/` takes a multipart form with one or more `file` fields (`chunk-XXXX.js.map`) and `Authorization: Bearer <token>`, with `SOURCEMAP_UPLOAD_TOKEN` or an API token. Uploading a file again replaces it. It answers with what it stored:

```json
{"release": "2026.11.19-1", "files": [{"name": "chunk-Z554BZQ6.js.map", "file": "chunk-Z554BZQ6.js", "size": 6177}]}
```

Errors come as `{"error": "..."}`: 401 without a token or with a wrong one, 404 if the project doesn't exist, 413 if a file is over 20 MiB, the request over 100 MiB, or a map takes more than the source map cache (64 MiB) once parsed (the message says how much), and 400 if a file isn't a valid v3 source map (with its name). If anything fails, nothing of that request is stored.

For example, in a `Dockerfile`, after building the front end and before deleting the `.map` files, one request per file to stay away from the 100 MiB limit, with the token as a BuildKit secret:

```dockerfile
RUN --mount=type=secret,id=sourcemap_upload_token,env=SOURCEMAP_UPLOAD_TOKEN \
    cd dist && \
    for f in *.map; do \
      curl -sS --fail-with-body \
        -H "Authorization: Bearer $SOURCEMAP_UPLOAD_TOKEN" \
        -F "file=@$f" \
        "$MOTHLOG_URL/api/$MOTHLOG_PROJECT_ID/releases/$SENTRY_RELEASE/sourcemaps/" || exit 1; \
    done && \
    rm -f *.map
```

`$SENTRY_RELEASE` has to be the same `release` the SDK sends and, if it has a `/`, encoded as `%2F`.

### Releases with sentry-cli

With the same variables as for source maps:

```sh
sentry-cli releases new "$SENTRY_RELEASE"         # creates it in SENTRY_PROJECT (or in those of -p)
sentry-cli releases finalize "$SENTRY_RELEASE"    # marks it as finished
sentry-cli deploys new -r "$SENTRY_RELEASE" -e production
```

| Route | What it does |
|---|---|
| `POST /api/0/organizations/{org}/releases/` | creates the release in each project of `projects`; if it already existed, leaves it as it was |
| `PUT /api/0/organizations/{org}/releases/{version}/` | finalizes it at `dateReleased` (or now) in the projects of `projects` or, without them, in every project that has it declared (404 if none) |
| `POST /api/0/organizations/{org}/releases/{version}/deploys/` | records the deploy to `environment` in every project that has the release declared (404 if none), at `dateFinished` or now; resolves the issues in `resolved_next` of an earlier release |

`sentry-cli deploys new` sends no projects. So that a deploy of one project doesn't resolve another's issues, without projects it only reaches declared releases: those created, finalized or deployed with a token (`sentry-cli`, the API or the command line) or imported, not those only created by an event with the DSN's public key. That is why it is worth running `sentry-cli releases new` (or `finalize`) before `deploys new`.

`tools/sentry-cli/prueba.sh` tests all of this with the real `sentry-cli` 3.8.2 against the binary, and `testdata/sentry-cli/releases/` is what it sent.

## Cron check-ins

The SDKs' cron monitoring (`@monitor` in Python, `Sentry.withMonitor` in JavaScript) and `sentry-cli monitors run` send check-ins to the same DSN: see [Cron monitors](monitors.md#cron-monitors).
