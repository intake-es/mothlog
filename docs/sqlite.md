# SQLite

Mothlog runs on Postgres 16 or on SQLite, as a single binary without a database server. This page explains how to run it on SQLite and its limits, and then, for contributors, what is specific to Postgres in the code and how it is done with SQLite.

## Running on SQLite

With `DATABASE_URL=sqlite:///path/mothlog.db` (or `sqlite://./mothlog.db`, relative to the working directory) everything is stored in a SQLite file, without Postgres, with every feature: ingestion, source maps, crons, uptime, alerts, the API, the MCP and the UI. The same binary and image work for both; the file and its schema (`migrations/sqlite/`) are created on start.

```sh
DATABASE_URL=sqlite:///var/lib/mothlog/mothlog.db PUBLIC_URL=https://errors.example.com ./mothlog
```

With Docker, put the file in a volume mounted on `/home/nonroot`, which in the image belongs to the user the service runs as:

```sh
docker run -p 8000:8000 -v mothlog:/home/nonroot -e DATABASE_URL=sqlite:///home/nonroot/mothlog.db \
  -e PUBLIC_URL=https://errors.example.com ghcr.io/intake-es/mothlog:latest
```

To try it out like the [quick start](../README.md#quick-start), but without Postgres: `docker compose -f docker-compose.sqlite.yml up` (the data goes in the `mothlog` volume; `docker compose -f docker-compose.sqlite.yml logs mothlog | grep dev/login` gives the sign-in link).

Limits:

- **A single instance.** SQLite allows one writer and the file lives on one machine: no replicas behind a load balancer. On start, the service takes a lock on `mothlog.db.lock`, and a second instance on the same file refuses to start ("another instance has the database open…"). The [commands](cli.md) (`projects`, `tokens`, `people`, `releases`, `import`) can run while the service is running. For several replicas, use Postgres.
- The file has to be on a local disk, not a network one (NFS, SMB), where SQLite's locks can't be trusted.
- There is no way to move from Postgres to SQLite or back: each database starts empty, although `import sentry` can bring what is in a Sentry.

It runs in WAL mode (next to `mothlog.db` are `mothlog.db-wal` and `mothlog.db-shm`, which are part of the database), with one writing connection and several reading ones. With the mix of `make carga` and 0.25 vCPU it takes the same as with Postgres ([Batches and performance](ingestion.md#batches-and-performance)).

### Backups

Copying the file while the service runs can give a broken copy; the two good ways work hot, without stopping it:

```sh
sqlite3 /var/lib/mothlog/mothlog.db ".backup '/backups/mothlog-$(date +%F).db'"
sqlite3 /var/lib/mothlog/mothlog.db "VACUUM INTO '/backups/mothlog-$(date +%F).db'"
```

`.backup` copies the database page by page; `VACUUM INTO` writes a compacted copy. Both give a single file, without `-wal`, which is restored by stopping the service and putting it in the place of `mothlog.db` (deleting the old `mothlog.db-wal` and `mothlog.db-shm` first). With Docker, the image has no `sqlite3`: run it from another container with the same volume, like `docker run --rm -v mothlog:/data -v "$PWD":/backups alpine sh -c "apk add -q sqlite && sqlite3 /data/mothlog.db \".backup '/backups/mothlog.db'\""`. `sqlite3` is fine for reading and copying the database, not for writing to it: the service registers its own functions (`now()`, a Unicode-aware `lower` and the `en_us` collation) that `sqlite3` doesn't have.

## How it is built

Database access goes through `internal/db`, which has an implementation for each engine (`postgres.go` and `sqlite.go`), and the SQL that differs between engines is kept in one place. The rest of this page says what is specific to Postgres, where it is, and how it is done with SQLite.

### A single instance

Several parts count on several replicas with Postgres and rely on `FOR UPDATE SKIP LOCKED` not to step on each other (sending alerts, the Linear and GitHub sync, the cron sweeps, uptime checks, retention, the conversion of old events and the digest). With a single instance and a single writer, those locks aren't needed.

How it is done (`internal/db/sqlite.go`, with `modernc.org/sqlite`, pure Go: the binary is still built with `CGO_ENABLED=0`):

- `DATABASE_URL=sqlite:///path/mothlog.db` (or `sqlite://./mothlog.db`, relative) picks SQLite; `postgres://…` picks Postgres.
- Two `*sql.DB` on the same file: a writing one with **a single connection**, whose transactions start with `BEGIN IMMEDIATE` (they take the write lock when they start: what `FOR UPDATE` does in Postgres, that lock gives), and a reading one with several connections in `query_only`. Standalone queries that only read (`SELECT`, `VALUES` or `WITH` without writes) go to the reading ones; the rest, to the writing one. Read-only transactions (`TxOptions{ReadOnly: true}`) go to a reading one.
- On open: `journal_mode=WAL`, `busy_timeout=10000`, `foreign_keys=ON` and `synchronous=NORMAL`.
- Starting the service (`store.Open`) takes a file lock (`flock` on `<file>.lock`) and refuses to start if another process has it, with a message that says so. The commands (`store.OpenForCommand`: `projects`, `tokens`, `people`, `releases`, `import`) don't take it, so they can run with the service started, as with Postgres.
- As in Postgres, a database error leaves the transaction aborted: what follows fails and `Commit` rolls it back, back to an earlier savepoint. SQLite on its own only undoes the failing statement and lets you go on; the code counts on Postgres's behaviour (an alert that couldn't be recorded must not stay half done).
- `Begin` inside a transaction opens a savepoint, as with pgx. A `SendBatch` outside a transaction runs in one, as Postgres does with a batch.

### The dialect: each function, where it is used and what it is in SQLite

`db.Dialect` (`internal/db/dialect.go`) gives the pieces of SQL that are written differently in each engine; `db.SQLiteDialect` is in `internal/db/dialect_sqlite.go`. Each query asks its `Querier` for it (`tx.Dialect()`, `s.pool.Dialect()`). `TestPostgresDialect` and `TestSQLiteDialect` check what each one writes. Paths are relative to `internal/`; the function is in parentheses.

| Function | Postgres | Where | SQLite |
|---|---|---|---|
| `ForUpdate(of...)` | `FOR UPDATE [OF t]` | `ingest/batch.go` (readBatch); `ingest/ingest.go` (lockIssue); `ingest/sessions.go` (IngestSession); `store/assign.go` (AssignIssue); `store/checkins.go` (saveCheckIn); `store/history.go` (changeStatusTx); `store/imports.go` (ImportIssue); `store/links.go` (ApplyExternalStatus); `store/monitors.go` (lockMonitor); `store/releases.go` (AddDeploy); `store/uptime.go` (UpdateUptimeMonitor, RecordUptimeCheck) | Empty: the transaction already holds the write lock (`BEGIN IMMEDIATE`). |
| `ForUpdateSkipLocked(of...)` | `FOR UPDATE [OF t] SKIP LOCKED` | `alert/alert.go` (sendOne); `alert/sync.go` (syncOne); `ingest/retention.go` (purgeEmptyIssues); `store/checkins.go` (sweepTimeout, sweepMissed); `store/convert.go` (convertBatch); `store/digests.go` (EnqueueDigest); `store/uptime.go` (ClaimUptimeChecks) | Empty: with a single instance there is no other replica whose rows to skip. |
| `InList(expr, $n)` | `expr = ANY($n)` with a Go slice | `alert/alert.go`, `alert/rules.go`, `ingest/retention.go`, `store/bundles.go`, `store/health.go`, `store/links.go`, `store/releases.go`, `store/uptime.go` | `expr IN (SELECT value FROM json_each($n))`; the `db` implementation turns slice arguments (`[]int64`, `[]string`…; `[]byte` is still a BLOB) into JSON text. |
| `InBytes(expr, $n)` | `expr = ANY($n)` | `store/issues.go` (termSQL, `user:`) | `expr IN (SELECT unhex(value) FROM json_each($n))`: a `[][]byte` goes as JSON with each element in hex. It is separate from `InList` because the dialect doesn't know the element type. |
| `Unnest(alias, $n, "col type"...)` | `unnest($1::bigint[], $2::text[]) AS k(a, b)` | `ingest/batch.go`, `ingest/tags.go`, `store/convert.go`, `store/eventdata.go`, `store/filters.go`, `store/issues.go`, `store/monitors.go`, `store/releases.go`, `store/uptime.go` | `(SELECT j.value AS a, ($2 ->> j.key) AS b FROM json_each($1) j) AS k`: it walks the first list and takes from the others the element at the same position with `->>`; `bytea` columns go with `unhex(…)`. Joining the `json_each` by `key` would be an n×n product without an index, which shows with batches of hundreds of events. |
| `UnnestNumbered(alias, num, …)` | `unnest(…) WITH ORDINALITY AS e(…, n)` | `store/eventdata.go` (InsertEventsQuery) | The same subquery with `j.key + 1 AS n`. |
| `Greatest`, `Least` | `GREATEST(a, b)`, `LEAST(a, b)` | `ingest/batch.go`, `ingest/ingest.go`, `ingest/sessions.go`, `ingest/tags.go`, `store/digest_data.go`, `store/imports.go`, `store/releases.go`, `store/uptime.go` | `max(COALESCE(a, b), COALESCE(b, a))` and `min(…)`: always with the `COALESCE`, because in SQLite they give NULL if an argument is NULL and in Postgres they ignore it. |
| `ILike(expr, p)` | `expr ILIKE p` | `store/issues.go` (termSQL); `store/tags.go` (TagValues) | `lower(expr) LIKE lower(p) ESCAPE '\'`, with a Unicode-aware `lower` registered in the driver. The `ESCAPE` is needed because the patterns escape `%` and `_` with `\` and SQLite has no default escape character. |
| `SecondsBetween(to, from)` | `EXTRACT(EPOCH FROM to - from)` | `store/digest_data.go`, `store/metrics.go`, `store/uptime.go` | `(CAST(to - from AS REAL) / 1000000)`, with dates in microseconds. |
| `AddSeconds`, `SubSeconds`, `AddMinutes` | `ts ± make_interval(secs => n)` | `store/checkins.go`, `store/links.go`, `store/uptime.go` | `ts ± CAST(round(n * 1000000) AS INTEGER)` (or `60000000` per minute). |
| `JSONText`, `JSONType` | `doc->>'k'`, `jsonb_typeof(doc->'k')` | `store/rules.go` (IssueRulesSQL) | `json_extract(doc, '$.k')` and `json_type(doc, '$.k')`. |
| `Collate(expr)` | `expr` | The text `ORDER BY`s of `store` (tags, environments, names, slugs, tokens…) | `expr COLLATE en_us`: see [Text ordering](#text-ordering). |
| `Typed(expr, type)` | `expr::type` | `alert/sync.go`, `ingest/batch.go`, `store/imports.go`, `store/issues.go`, `store/uptime.go` | `expr`: the type only tells Postgres. |

`db.ByEngine(d, postgres, sqlite)` picks between two whole queries when pieces aren't enough.

### What is still Postgres-specific in the queries

Each has its SQLite version next to it, with `db.ByEngine` or with `s.sqlite()`.

| Construct | Where | SQLite |
|---|---|---|
| `LATERAL` | `alert/alert.go` (loadIssue); `store/digest_data.go` (DigestRegressions); `store/issues.go` (issueSQL, IssueDetailIn); `store/monitors.go` (ListMonitors); `store/releases.go` (releaseFrom, ReleaseDetail ×2); `store/uptime.go` (ListUptimeMonitors) | The per-row aggregates of `issueSQL`, `releaseFrom` and the one for issues that stopped in `ReleaseDetail` go in a `JOIN` with a non-aggregated subquery (`SELECT x.id, (SELECT sum(…) …) AS n … FROM table x`), which SQLite flattens and computes row by row with the index, with the same column names as the Postgres one; the issues of a release in `ReleaseDetail`, in a `JOIN` with the release's counts grouped by issue. "The last row of each" (`loadIssue`, `DigestRegressions`) becomes a scalar subquery with `max(id)` or `ORDER BY … LIMIT 1`, with an `EXISTS` where the `CROSS JOIN LATERAL` left out those without one. "The last N of each monitor" (`ListMonitors`, `ListUptimeMonitors`) becomes `c.id IN (SELECT id … WHERE monitor_id = m.id ORDER BY … LIMIT N)`: it reads only those N through the index, while `ROW_NUMBER() OVER (PARTITION BY …)` would go through all of each monitor's. The `CROSS JOIN LATERAL (VALUES …)` of `IssueDetailIn` becomes a `UNION ALL` of the four columns. |
| `generate_series`, `to_char`, `AT TIME ZONE 'UTC'`, `::date`, `interval '1 day'` | `store/issues.go` (DailyCountsIn); `store/filters.go` (AddDropped, DroppedSince) | Days with a recursive CTE in microseconds of their 00:00 UTC (`($2 / 86400000000 - n + 1) * 86400000000` and `+ 86400000000`); their text with `strftime('%Y-%m-%d', day / 1000000, 'unixepoch')`. `dropped_events.day` keeps the integer of 00:00 UTC. |
| `date_bin($3::interval, checked_at, $2)` | `store/uptime.go` (UptimeDetail) | `$2 + (checked_at - $2) / $3 * $3`, with the step in microseconds. |
| `GROUPING SETS` | `store/health.go` (loadHealth) | Two queries joined with `UNION ALL`: per release and environment, and per release. |
| `bool_or` | `store/releases.go` (ReleaseDetail) | `max(expr)`. |
| `INSERT` inside a `WITH` | `ingest/tags.go` (recordTags); `ingest/batch.go` (tagsQueries) | SQLite doesn't allow writes in a CTE: they go as consecutive statements, in the same transaction with the write lock. To count new users, first the ones that weren't there are added up, then they are saved. |
| `UPDATE t a`, `DELETE FROM t a` | `ingest/batch.go`, `ingest/retention.go`, `store/monitors.go`, `store/uptime.go` | SQLite needs `AS` in the alias (`UPDATE issues AS i`), which Postgres accepts too: it is written that way for both. In an `UPDATE … FROM … RETURNING`, SQLite doesn't allow naming the table in the `RETURNING`: `ClaimUptimeChecks` calls the subquery's column `due_id` so that `RETURNING id, …` isn't ambiguous in Postgres. |
| `INSERT … SELECT … FROM x ON CONFLICT` | `ingest/batch.go`, `ingest/tags.go`, `store/releases.go` (TrackEventsQuery) | It has a `WHERE true` before the `ON CONFLICT`, which SQLite needs so as not to read the `ON` as a `JOIN` condition; Postgres accepts it as well. |
| `::uuid`, `replace(CAST(uuid AS text), '-', '')` | `alert/alert.go`, `alert/cron.go`, `ingest/batch.go`, `store/issues.go`, `store/monitors.go` | UUIDs are stored as text, as Go gives them: `events.event_id` and `monitor_checkins.check_in_id` as 32 lowercase hex characters, `sourcemaps.debug_id` with dashes (as `internal/sourcemap` normalizes it) and `alert_outbox.linear_create_id` with dashes, because it is read with `CAST(… AS text)` and sent to Linear as a UUID. The `replace(CAST(… AS text), '-', '')` works as is. `Event` turns the reference into 32 lowercase hex characters before looking it up. The `gen_random_uuid()` in the `DEFAULT` of `linear_create_id` is, in SQLite, an expression with `randomblob` that gives a v4 UUID. |
| `now()` | Almost every write | `now()` is a function registered in the driver that gives the time in microseconds. Unlike Postgres, it isn't the same for the whole transaction, but the current one. The schema's `DEFAULT (now())` use it. |
| JSON of old events: `e.data->'user'->>'id'`, `jsonb_array_length`, `btrim`, `concat_ws` | `store/digest_data.go` (legacyUserIdent); `store/issues.go` (legacyUserLabel, legacyOrganizationID, legacySummary) | A SQLite database is born with every event compressed: with SQLite those expressions are `NULL` (`legacy(d, …)`), and `ConvertLegacyEvents` and `RunEventConversion` do nothing. That's why `btrim` and `concat_ws` don't need registering. |
| Arrays as columns (`text[]`) | `projects.linear_label_ids`; `project_filters.*` | JSON text. The `db` implementation writes a `[]string` as a JSON array and reads it back. `Filters` no longer uses `COALESCE(…, '{}')`: for a project without filters it leaves the lists empty in Go, the same with both engines. |
| `jsonb` as columns | `alert_rules.conditions`, `actions`; `alert_outbox.detail`; `project_digests.destinations`; `import_origins.imported` | JSON text: structs and maps are written with `json.Marshal` and read with `json.Unmarshal`. JSON that already comes as bytes has to go as `json.RawMessage` (which goes as text), not as `[]byte` (which is a BLOB): that is how `enqueueRule`, `EnqueueDigest` and `saveOrigin` pass it; Postgres stores it the same. |
| `RETURNING`, `ON CONFLICT … DO UPDATE/NOTHING` (also with a partial index), `FILTER (WHERE …)`, `count(*) OVER ()`, `IS DISTINCT FROM`, row comparisons `(a, b) < ($1, $2)` | All over `store`, `alert` and `ingest` | They work in SQLite as they are (the driver bundles SQLite 3.54). |

### Dates and types

Dates are stored as **integers of microseconds since 1970 in UTC**: they compare and sort as numbers, subtracting gives microseconds, and no precision is lost compared with Postgres. The `db` implementation (`internal/db/sqlite_convert.go`) converts the arguments (`time.Time`, slices, structs, maps, pointers, named types) and what is read (`*time.Time`, `**time.Time`, `*[]string`, JSON structs, `*bool` from 0 and 1…), so `store` keeps working with `time.Time`, `[]string` and its structs. Dates read come in the local zone, as pgx gives them.

Tables are `STRICT`: SQLite refuses to store text in a date column or a BLOB in a JSON one, instead of storing it and comparing it wrong later.

### Text ordering

Postgres sorts text with the database's collation (`en_US.UTF-8` in the `postgres:16` image): ignoring punctuation and leading spaces, with capitals and accents after; SQLite, by bytes. So that the API, the MCP, the Markdown and the digest come out the same with both engines, the driver registers the `en_us` collation (`internal/db/sqlite_collate.go`): it compares letters and numbers with `golang.org/x/text/collate` (English), breaks ties by punctuation in glibc's order and, at the end, by bytes, as Postgres does. `TestCollateLikePostgres` checks the order Postgres 16 gives for a list of strings. It is only used in `ORDER BY` (`Dialect.Collate`), not on columns, so it doesn't cost on every index and `sqlite3` can copy the database without knowing it.

### Migrations and start-up

- The migrations in `migrations/` are Postgres's. SQLite has its own in `migrations/sqlite/`: `00022_esquema.sql` is the whole schema as of the Postgres migration 00022, without repeating the history, and each later migration has a SQLite version with the same number (`00023_…` onwards), so a new migration takes the same number in both engines. The schema adds indexes Postgres doesn't have on some foreign keys (`alert_outbox.issue_id`, `rule_id`, `waits_for`, `issue_links.rule_id`, `issue_link_sync.link_id`), because SQLite looks up the rows that reference one being deleted.
- `store.Open` migrates with goose in the `sqlite3` dialect and without a lock (there is a single instance); with Postgres, with `pg_advisory_lock`.

### Errors

`db.IsUniqueViolation` and `db.IsForeignKeyViolation` look at the Postgres code (`23505`, `23503`) or at SQLite's extended codes (`SQLITE_CONSTRAINT_UNIQUE` or `PRIMARYKEY`, and `SQLITE_CONSTRAINT_FOREIGNKEY`).

### Tests

`storetest` uses Postgres if `MOTHLOG_TEST_DATABASE_URL` is set and, otherwise, SQLite in a temporary file. With SQLite, the tests that only make sense with Postgres are skipped, saying why: Postgres migrations over old data, converting and reading events stored as jsonb, several replicas at once with `SKIP LOCKED`, retention skipping an issue locked by another transaction (with SQLite, `TestPurgeKeepsEventBeingIngested` checks that retention waits and the event isn't lost) and an event with the NUL character, which Postgres rejects and SQLite stores. How to run them is in [CONTRIBUTING.md](../CONTRIBUTING.md#tests).
