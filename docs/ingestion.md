# Ingestion

SDKs send to `POST /api/<project id>/envelope/` with the DSN's key in `X-Sentry-Auth` or in the `sentry_key` query parameter. Mothlog accepts up to 1 MiB compressed, 10 MiB decompressed and 100 items per envelope; past any of the three, 413. It keeps `event` items, counts `session` and `sessions` items ([Release health](sdks.md#release-health)) and reads `check_in` items ([Cron monitors](monitors.md#cron-monitors)); everything else is accepted and ignored.

Past the project's per-minute limit (`RATE_LIMIT_PER_MINUTE` unless the project sets its own), it answers 429 with `Retry-After` and `X-Sentry-Rate-Limits`. The limit is counted in memory, per replica. Sessions have a separate limit, 10 times that one.

The release and environment of events, sessions and check-ins follow the API's rules: no surrounding spaces, not empty, at most 200 characters, and no line breaks or tabs. They are checked when the event is read, before anything else: an event with one that breaks the rules is kept without it, as Sentry does, and counted with the reason `invalid_release` or `invalid_env` (without adding to the dropped total, since the event is kept). A session with such a release or environment isn't counted (reason `session_invalid`). A check-in loses its release; with such an environment it is ignored entirely, because without an environment it would count as `production`.

## Grouping

Each event is grouped into an issue by its fingerprint: the SDK's `fingerprint` if it brings one; otherwise the type of the raised exception and the module or file and function of its `in_app` frames (without line numbers); and for logs without an exception, the logger and the message with UUIDs, hex strings and numbers replaced by placeholders. An event in a `resolved` issue, or in a `resolved_next` one with a later release, reopens it as a regression ([Releases](sdks.md#releases-deploys-and-environments)).

## Tags and users

With each event, each issue counts the values of its tags and when each was last seen: release, environment, level, `browser`, `os` and `device` (from `contexts` or, if they don't come, from the user agent), `runtime`, and up to 50 SDK `tags`, with keys of up to 32 characters and values cut to 200.

It also counts distinct users, by id, email, username or IP, in that order. For that count it keeps only the first 16 bytes of a SHA-256 keyed with a random per-installation key (table `user_hash_key`): the count doesn't keep the value in clear, although the stored event still carries it until retention deletes it. A duplicate event doesn't count twice; the ones over the cap or outside the sample do count, as in the event count.

## Retention

Every hour, events older than `RETENTION_DAYS` days are deleted, and so are issues left without events.

## Filters, cap and sampling

Each project has its own settings, with [`mothlog projects filters`](cli.md#projects), in the UI (project settings) or with `/api/0/projects/{id}/filters`. By default it filters nothing, keeps at most 100 events per issue and hour, doesn't sample and uses `RATE_LIMIT_PER_MINUTE`.

Filters are applied when the event arrives, before grouping: a filtered event doesn't open or count in any issue, doesn't alert and doesn't use up the per-minute limit.

| Key | Drops | Reason in the counts |
|---|---|---|
| `messages` | events whose message, or the `Type: value` or value of any exception, matches any of these regular expressions (Go syntax; `(?i)` for case-insensitive) | `message` |
| `exception_types` | those with an exception of these types, by name (`KeyError`) or with the module in front (`payments.errors.RefundRejected`) | `exception_type` |
| `browser_extensions` (`on`/`off`) | errors from browser extensions: `chrome-extension://`, `moz-extension://`, `safari-extension://`, `safari-web-extension://`, `ms-browser-extension://` or `edge-extension://` in a frame, the message or an exception | `browser_extension` |
| `web_crawlers` (`on`/`off`) | those from requests by bots, crawlers and uptime checkers, by the `User-Agent` in the event's `request.headers` | `web_crawler` |
| `localhost` (`on`/`off`) | those with `request.url` on `localhost`, `*.localhost`, `127.0.0.0/8`, `::1` or `0.0.0.0`, or with the user's IP in one of those | `localhost` |
| `environments` | those from these environments | `environment` |
| `releases` | those from these releases; `*` matches anything (`*-beta.*`) | `release` |

The rest limits what is kept without losing the count:

- `events_per_issue_per_hour` (100 by default): past the cap within a clock hour, the event isn't kept, but the issue adds it to its `count` and last seen, so the number is the real one. It doesn't alert again. Reason `issue_limit`.
- `sample_rate`, from 0 to 1 (1 by default): the share of events of existing issues that is kept; the ones left out count in the issue like those over the cap. Reason `sampled`.
- `rate_limit_per_minute`: the project's per-minute limit, instead of `RATE_LIMIT_PER_MINUTE`. What goes over it is rejected with a 429 and counts in no issue. Reason `rate_limit`.

The first event of a new issue and that of a regression are always kept, whatever the cap or the sampling say. Sampling also lets through the first event of each day (UTC) of each issue, so an issue that keeps getting events always has one kept within `RETENTION_DAYS` and retention doesn't delete it with its `count`. If an event that wasn't kept is sent again with the same `event_id`, it counts again, because nothing is left to recognize it by; SDKs only resend when the answer isn't a 200.

Dropped counts are kept in memory and saved every 10 seconds and on shutdown, by project, reason and day (UTC); if the process dies without shutting down, those of the last seconds are lost. `mothlog projects dropped <name>` and `GET /api/0/projects/{id}/dropped` show them.

## How an event is stored

Each event is stored normalized (what `GET /api/0/events/{id}/` and the MCP's `get_event` return) as zstd-compressed JSON in `events.data_zstd`. The little that SQL queries need goes in its own columns: `user_label` (the user that is shown: id, email, username or IP, the first there is), `user_ident` (the user as the digest counts it), `organization_id`, and `summary` (the "Type: value" or the message of the event list). The rest (stack trace, breadcrumbs, contexts) is only read by Go, whole, so no query depends on `jsonb` or Postgres functions to read the event.

It is compressed with a zstd dictionary built from the events in `testdata/envelopes` (`internal/store/zstd-dict-1.jsonl`, inside the binary). Each event carries in its header the id of the dictionary it was compressed with: to change it, add another one with a new id and keep the old one, which still reads the old events; a test prevents changing one already in use. There is a single encoder and a single decoder per process, with two reused states (about 3.5 MB in total).

How much it saves, measured with Postgres 16 after `VACUUM FULL` (`pg_total_relation_size('events')`, with its indexes):

| Events | Before (`jsonb`) | Now | Saving |
|---|---|---|---|
| The 10 fixtures | 128 KiB (table and TOAST: 48 KiB) | 96 KiB (16 KiB) | 25 % (67 % without the indexes, which with so few rows are minimal pages) |
| 5,010 generated from the fixtures | 12.0 MiB (`jsonb`: 9.8 MiB) | 2.6 MiB (zstd: 0.6 MiB) | 79 % |

Those are events from the same SDKs and apps as the dictionary, the best case. With events from another app, zstd without a dictionary leaves the JSON at 32 % of its size (against `jsonb`, which Postgres already compresses with pglz above 2 KB) and the dictionary takes off another 10-20 %.

Events stored by versions before compression are still in `events.data` (`jsonb`). On start, the service converts them in the background, in batches of 200 with a transaction each, without blocking the start; replicas share the rows with `FOR UPDATE SKIP LOCKED`, and if it stops halfway, what was converted stays and the rest is converted on the next start. While any event is left unconverted, it repeats every hour, to pick up what a replica of the previous version stores during a rolling deploy, or what one that dies leaves locked; it ends when none is left, counting those locked by another replica. Meanwhile both formats are read. The space of converted rows is reused for new events after autovacuum; to give it back to the disk, `VACUUM FULL events` (which locks the table) or `pg_repack`.

## Batches and performance

The events of envelopes that arrive while a batch is being written go together in the next one, in one transaction: duplicates are looked up and issues (once each, by id) and releases are locked in one round trip to the database; each event is decided in memory and in order, as one by one (grouping, regression, cap, sampling); and the events, each issue's counters, the counts per release and environment, the tags and the users are written at once in another. Alerts are queued event by event within the same transaction. The response waits for its batch to commit: a 200 still means stored. If the batch fails, its events are saved one by one, so only the bad one fails. A batch holds at most `INGEST_BATCH_MAX` events (those of one envelope are never split) and waits up to `INGEST_BATCH_WAIT` only if another envelope is halfway read; with a single envelope, it is written right away.

`make carga` measures ingestion: it builds the image from the `Dockerfile`, runs it with `--cpus=0.25 --memory=256m` next to a Postgres 16 in another container, uploads the fixtures' source maps and sends envelopes made from `testdata/envelopes` with new ids: 45 % from four hot errors, 20 % from a tail of 5,000 distinct errors, 15 % from the browser error with source maps, 5 % check-ins and 15 % sessions. It goes in concurrency steps (`-concurrencia 1,4,16,64`, 20 s each) or at a fixed rate (`-ritmo 300`), and leaves in `carga-salida/` `informe.md` and `informe.json` with the accepted events per second, p50, p95 and p99 latencies, status codes, CPU and how much the cgroup throttled it, mean and maximum RSS, the pool, what the Postgres connections wait on (`pg_stat_activity`), a CPU profile per step (`cpu-*.pprof` and its `go tool pprof -top`) and a check that what was stored matches what was accepted. It removes the containers, the network and the image it built when it ends, also on Ctrl-C; with `-conservar-imagen` it keeps the image, to repeat the measurement with `-construir=false`. It needs Docker on the same machine, because it reads the process's memory and CPU from `/proc` and its cgroup. To measure another commit: `make carga CARGA='-ref origin/main -salida carga-main'`. With `-motor sqlite` the service uses a SQLite file in a mounted directory, without a Postgres container, and the report has no `pg_stat_activity` section: `make carga CARGA='-motor sqlite -salida carga-sqlite'`.

Measured on 2026-11-23 on a 16-thread desktop CPU (WSL2, kernel 6.6, Docker 29.9), with the same command and 20 s steps:

| Concurrency | Before batches: events/s | p99 | max RSS | With batches: events/s | p99 | max RSS |
|---|---|---|---|---|---|---|
| 1 | 130 | 54 ms | 45 MB | 142 | 51 ms | 46 MB |
| 4 | 172 | 88 ms | 46 MB | 214 | 80 ms | 47 MB |
| 16 | 193 | 322 ms | 52 MB | 284 | 99 ms | 54 MB |
| 64 | 195 | 703 ms | 56 MB | 323 | 298 ms | 59 MB |

With 0.25 vCPU and under 50 MB it takes 214 events per second (172 before); at most, 323 (195 before). In every step the CPU is at its 25 % cap. Before, 41 % of the profile was saving the event, almost all of it writing to the Postgres socket (about ten statements per event), and with concurrency the transactions waited for the row lock of the hot issue (`Lock:tuple` and `Lock:transactionid`, 7 to 9 connections on average) with the pool exhausted (929 s of waiting at 64). With batches, writing them is 4 % of the profile (10 % with preparing each event: source maps, fingerprint and zstd), and what weighs most is parsing the event's JSON (31 %) and the two database reads of each envelope, the project and its filters (20 %); lock waits drop to one connection. Above about 16 concurrent envelopes, RSS goes over 50 MB, before and after: it grows with the envelopes in flight, not with the batches.

With SQLite (`-motor sqlite`), the same image and the same mix, one run of each engine back to back on 2026-11-23; with Postgres the database runs in another container without a CPU limit, and with SQLite inside the service's 0.25 vCPU:

| Concurrency | SQLite: events/s | p99 | max RSS | Postgres: events/s | p99 | max RSS |
|---|---|---|---|---|---|---|
| 1 | 153 | 72 ms | 51 MB | 147 | 50 ms | 45 MB |
| 4 | 216 | 90 ms | 55 MB | 209 | 82 ms | 48 MB |
| 16 | 274 | 106 ms | 61 MB | 279 | 100 ms | 53 MB |
| 64 | 324 | 396 ms | 63 MB | 325 | 295 ms | 59 MB |
