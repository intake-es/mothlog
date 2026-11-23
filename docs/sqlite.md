# Hacia SQLite: lo que es propio de Postgres

Hoy intake-errors solo corre con Postgres 16. Para que pueda correr también con SQLite (un solo binario, sin servidor de base de datos), el acceso a la base pasa por `internal/db` y el SQL que cambia de un motor a otro está localizado. Esta página dice qué queda propio de Postgres, dónde está y cómo se hará con SQLite.

## Con SQLite, una sola instancia

SQLite admite un solo escritor a la vez y el fichero vive en una máquina, así que **con SQLite el servicio corre en una sola instancia**: nada de réplicas detrás de un balanceador. Varias piezas cuentan hoy con varias réplicas y se apoyan en `FOR UPDATE SKIP LOCKED` para no pisarse (el envío de avisos, la sincronización con Linear y GitHub, los barridos de crons, las comprobaciones de disponibilidad, la retención, la conversión de eventos antiguos y el resumen). Con una sola instancia y un solo escritor, esos cerrojos sobran.

Cómo se hará:

- La implementación de `db.DB` para SQLite abre las transacciones de escritura con `BEGIN IMMEDIATE`, que coge el cerrojo de escritura al empezar: lo que hoy hace `FOR UPDATE` (leer, decidir y escribir sin que otro cambie la fila en medio) lo da ese cerrojo.
- Una sola conexión de escritura (o `SetMaxOpenConns(1)` para escribir) y `PRAGMA journal_mode=WAL`, `busy_timeout`, `foreign_keys=ON` y `synchronous=NORMAL` al abrir.
- El arranque se niega si hay otra instancia con el mismo fichero (un cerrojo de fichero), para que nadie monte dos réplicas sin darse cuenta.

## Lo que ya está hecho

- **Nadie fuera de `internal/db` e `internal/store` importa pgx.** El resto del código usa `db.DB`, `db.Tx` (con savepoints con `Begin` dentro de una transacción), `db.Rows`, `db.Batch`, `db.BeginFunc`, `db.CollectRows`, `db.ErrNoRows`, `db.IsUniqueViolation` y `db.IsForeignKeyViolation`, y el estado del pool para `/metrics` sale de `db.Stats`. `TestOnlyDBAndStoreImportPgx` (`internal/db/imports_test.go`) falla si alguien importa pgx en otro sitio. El código de `internal/store` ya no usa pgx salvo en sus tests; la implementación de SQLite solo tiene que dar otro `db.DB`.
- **Conversiones portables.** Las conversiones a `bigint`, `integer`, `text`, `boolean` y `double precision` se escriben con `CAST(x AS tipo)`, que vale en los dos motores (con SQLite, `CAST` a esos nombres de tipo da la afinidad INTEGER, TEXT, NUMERIC o REAL que toca).
- **El dialecto.** `db.Dialect` (`internal/db/dialect.go`) da los trozos de SQL que se escriben distinto en cada motor. Cada consulta lo pide a su `Querier` (`tx.Dialect()`, `s.pool.Dialect()`), así que la implementación de SQLite solo tiene que devolver su propio `Dialect`. Hoy solo existe `db.PostgresDialect`, y `TestPostgresDialect` comprueba que escribe exactamente lo que estaba escrito a mano.

## El dialecto: cada función, dónde se usa y cómo será en SQLite

Las rutas son relativas a `internal/`; entre paréntesis, la función.

| Función | Postgres | Dónde | SQLite |
|---|---|---|---|
| `ForUpdate(of...)` | `FOR UPDATE [OF t]` (12) | `ingest/batch.go` (readBatch); `ingest/ingest.go` (lockIssue); `ingest/sessions.go` (IngestSession); `store/assign.go` (AssignIssue); `store/checkins.go` (saveCheckIn); `store/history.go` (changeStatusTx); `store/imports.go` (ImportIssue); `store/links.go` (ApplyExternalStatus); `store/monitors.go` (lockMonitor); `store/releases.go` (AddDeploy); `store/uptime.go` (UpdateUptimeMonitor, RecordUptimeCheck) | Cadena vacía: la transacción ya tiene el cerrojo de escritura (`BEGIN IMMEDIATE`). |
| `ForUpdateSkipLocked(of...)` | `FOR UPDATE [OF t] SKIP LOCKED` (9) | `alert/alert.go` (sendOne); `alert/sync.go` (syncOne); `ingest/retention.go` (purgeEmptyIssues); `store/checkins.go` (sweepTimeout, sweepMissed); `store/convert.go` (convertBatch); `store/digests.go` (EnqueueDigest); `store/uptime.go` (ClaimUptimeChecks) | Cadena vacía: con una sola instancia no hay otra réplica de la que saltarse filas. |
| `InList(expr, $n)` | `expr = ANY($n)` con un slice de Go (13) | `alert/alert.go` (sendOne); `alert/rules.go` (enqueueRule); `ingest/retention.go` (purgeEmptyIssues); `store/bundles.go` (MissingUploadChunks, BundleAssembled); `store/health.go` (loadHealth ×3); `store/issues.go` (termSQL); `store/links.go` (consumeEcho); `store/releases.go` (AddDeploy, loadDeploys); `store/uptime.go` (fillAvailability) | `expr IN (SELECT value FROM json_each($n))`; la implementación de `db` para SQLite pasa los slices de los argumentos (`[]int64`, `[]string`, `[][]byte`…; `[]byte` sigue siendo un BLOB) a un texto JSON. Los `[]byte` dentro de una lista van en hexadecimal y se comparan con `unhex(value)`. |
| `Unnest(alias, $n, "col tipo"...)` | `unnest($1::bigint[], $2::text[]) AS k(a, b)` (14) | `ingest/batch.go` (readBatch ×3, updateIssuesQuery, tagsQuery ×2); `ingest/tags.go` (recordTags); `store/convert.go` (convertBatch); `store/eventdata.go` (InsertEventsQuery); `store/filters.go` (AddDropped); `store/issues.go` (DailyCountsIn); `store/monitors.go` (ListMonitors); `store/releases.go` (TrackEventsQuery); `store/uptime.go` (ListUptimeMonitors) | Una subconsulta con un `json_each` por lista, unidas por posición: `(SELECT a.value AS a, b.value AS b FROM json_each($1) a JOIN json_each($2) b USING (key)) AS k`, con los mismos argumentos pasados a JSON. Las inserciones por lotes (`InsertEventsQuery`, `TrackEventsQuery`, `tagsQuery`, `AddDropped`) pueden ir también como un `INSERT … VALUES (…), (…)` con varias filas, si medido sale mejor. |
| `UnnestNumbered(alias, num, …)` | `unnest(…) WITH ORDINALITY AS e(…, n)` (1) | `store/eventdata.go` (InsertEventsQuery) | La misma subconsulta con `a.key + 1 AS n`. |
| `Greatest`, `Least` | `GREATEST(a, b)`, `LEAST(a, b)` (16) | `ingest/batch.go` (updateIssuesQuery ×2, tagsQuery); `ingest/ingest.go` (save ×2); `ingest/sessions.go` (addUser); `ingest/tags.go` (recordTags); `store/digest_data.go` (DigestUptime); `store/imports.go` (ImportRelease, SettleImportedUsers); `store/releases.go` (TrackEvent ×2, TrackEventsQuery ×2); `store/uptime.go` (UpdateUptimeMonitor, RecordUptimeCheck) | `max(a, b)` y `min(a, b)` de varios argumentos. Ojo: en SQLite dan NULL si un argumento es NULL y en Postgres lo ignoran; donde un lado puede ser NULL, `max(COALESCE(a, b), COALESCE(b, a))`. |
| `ILike(expr, p)` | `expr ILIKE p` (2) | `store/issues.go` (termSQL); `store/tags.go` (TagValues) | `expr LIKE p`: el `LIKE` de SQLite ya no distingue mayúsculas en ASCII. Para que «Ñ» y «ñ» casen igual que en Postgres, `lower(expr) LIKE lower(p)` con una función `lower` propia que entienda Unicode. |
| `SecondsBetween(to, from)` | `EXTRACT(EPOCH FROM to - from)` (4) | `store/digest_data.go` (DigestUptime); `store/metrics.go` (MetricsSnapshot); `store/uptime.go` (UpdateUptimeMonitor, RecordUptimeCheck) | `(julianday(to) - julianday(from)) * 86400`, o la resta directa si las fechas se guardan como microsegundos desde 1970 (ver fechas, abajo). |
| `AddSeconds`, `SubSeconds`, `AddMinutes` | `ts ± make_interval(secs => n)` (3) | `store/checkins.go` (sweepMissed); `store/links.go` (consumeEcho); `store/uptime.go` (ClaimUptimeChecks) | `strftime(…, ts, '+' || n || ' seconds')`, o una suma de enteros con las fechas en microsegundos. |
| `JSONText`, `JSONType` | `doc->>'k'`, `jsonb_typeof(doc->'k')` (4) | `store/rules.go` (IssueRulesSQL) | `json_extract(doc, '$.k')` y `json_type(doc, '$.k')`; `json_type` dice `true`/`false` en vez de `boolean`, pero aquí solo se compara con `object`. |

## Lo propio de Postgres que sigue en las consultas

Esto no cabe en un trozo de SQL con nombre: son consultas enteras, o tipos de columna, que con SQLite se escriben de otra forma. Cada consulta está en una sola función; la siguiente tarea le da su versión de SQLite (con `d.Name()` o con otra constante de SQL al lado).

| Construcción | Dónde | SQLite |
|---|---|---|
| `LATERAL` (9) | `alert/alert.go` (loadIssue); `store/digest_data.go` (DigestRegressions); `store/issues.go` (issueSQL, IssueDetailIn); `store/monitors.go` (ListMonitors); `store/releases.go` (releaseFrom, ReleaseDetail ×2); `store/uptime.go` (ListUptimeMonitors) | SQLite no tiene `LATERAL`. Los agregados por fila (`issueSQL`, `releaseFrom`, `ReleaseDetail`) pasan a subconsultas correlacionadas en el `SELECT` o a un `LEFT JOIN` con una subconsulta agrupada por la clave. «La última fila de cada uno» (`loadIssue`, `DigestRegressions`) pasa a una subconsulta escalar con `ORDER BY … LIMIT 1`. «Las N últimas de cada monitor» (`ListMonitors`, `ListUptimeMonitors`) pasa a `ROW_NUMBER() OVER (PARTITION BY monitor_id ORDER BY …)` con `WHERE rn <= N`. El `CROSS JOIN LATERAL (VALUES …)` de `IssueDetailIn` pasa a un `UNION ALL` de las cuatro columnas. |
| `generate_series`, `to_char`, `AT TIME ZONE 'UTC'`, `::date`, `::timestamp`, `interval '1 day'` | `store/issues.go` (DailyCountsIn); `store/filters.go` (AddDropped, DroppedSince) | Los días con un CTE recursivo (`WITH RECURSIVE d(day) AS (SELECT date(?) UNION ALL SELECT date(day, '+1 day') …)`); el día UTC con `date(ts)` y su texto con `strftime('%Y-%m-%d', …)`. |
| `date_bin($3::interval, checked_at, $2)` | `store/uptime.go` (UptimeDetail) | División entera: `$2 + ((checked_at - $2) / paso) * paso`, con las fechas en microsegundos. |
| `GROUPING SETS` | `store/health.go` (loadHealth) | Dos consultas unidas con `UNION ALL`: por release y entorno, y por release. |
| `bool_or` | `store/releases.go` (ReleaseDetail) | `max(expr)`: en SQLite un booleano es 0 o 1. |
| `::timestamptz` en parámetros | `alert/sync.go` (syncOne ×2); `store/imports.go` (ImportRelease, createImportedIssue); `store/issues.go` (DailyCountsIn ×2); `store/uptime.go` (UpdateUptimeMonitor) | Sobra: solo le dice a Postgres el tipo del parámetro. |
| `::uuid`, `replace(CAST(uuid AS text), '-', '')` | `alert/cron.go` (loadCron); `ingest/batch.go` (readBatch); `store/issues.go` (storedEventColumns, Event, IssueEventsIn); `store/monitors.go` (checkInIDColumn) | Los uuid (`events.event_id`, `monitor_checkins.check_in_id`, `alert_outbox.linear_create_id`, `sourcemaps.debug_id`) se guardan como texto de 32 caracteres hexadecimales en minúsculas; el `replace` y la conversión sobran. `gen_random_uuid()` de los `DEFAULT` lo pone Go al insertar. |
| `::bytea` en parámetros | `ingest/tags.go` (recordTags) | Sobra; `bytea` es `BLOB`. |
| `now()` | Casi todas las escrituras de `store`, `alert` e `ingest` | SQLite no tiene `now()`. La implementación de SQLite registra una función `now()` propia en el driver, que da la hora en el mismo formato que las fechas guardadas, y el SQL no cambia. Lo mismo vale para `btrim` y `concat_ws` si el driver no las trae. |
| JSON de los eventos antiguos: `e.data->'user'->>'id'`, `jsonb_array_length`, `->-1`, `btrim(…, E' \t\n\r')`, `concat_ws` | `store/digest_data.go` (legacyUserIdent); `store/issues.go` (legacyUserLabel, legacyOrganizationID, legacySummary, scanStoredEvent) | Solo leen `events.data`, la columna de los eventos de antes de comprimirlos. Una base de SQLite nace con todos los eventos comprimidos, así que en SQLite esas expresiones son `NULL` y `ConvertLegacyEvents` no hace nada. |
| Arrays como columnas (`text[]`) | `projects.linear_label_ids`; `project_filters.messages`, `exception_types`, `environments`, `releases` (`store/projects.go`, `store/filters.go`) | Texto JSON. La implementación de `db` para SQLite lee y escribe un `[]string` como un array JSON, para que `store` no cambie. |
| `jsonb` como columnas | `alert_rules.conditions`, `actions`; `alert_outbox.detail`; `project_digests.destinations`; `import_origins.imported` | Texto JSON. Hoy pgx convierte los structs de Go a JSON y al revés; con SQLite lo hace la implementación de `db` (o el tipo con `Scanner`/`Valuer`). |
| `RETURNING`, `ON CONFLICT … DO UPDATE/NOTHING` (también con índice parcial), `FILTER (WHERE …)`, `count(*) OVER ()`, `IS DISTINCT FROM` | Por todo `store`, `alert` e `ingest` | Valen en SQLite tal cual (3.35 o más para `RETURNING`; 3.39 para `IS DISTINCT FROM`). No hay que tocarlas. Un `RETURNING` de un `INSERT … ON CONFLICT DO NOTHING` que no inserta tampoco da fila en SQLite, así que `db.ErrNoRows` sigue saliendo donde hoy. |

## Fechas

Postgres guarda `timestamptz` con microsegundos y compara fechas como fechas. En SQLite una fecha es texto, un número real o un entero. La propuesta es guardarlas como **enteros de microsegundos desde 1970 en UTC**: se comparan y ordenan como números, restar da microsegundos (lo que necesitan `SecondsBetween`, `date_bin` y los `make_interval`) y no se pierde precisión respecto a Postgres. La implementación de `db` para SQLite convierte `time.Time` en los argumentos y al leer, para que `store` siga trabajando con `time.Time`.

## Migraciones y arranque

- Las migraciones de `migrations/` son de Postgres (identidades, `timestamptz`, `jsonb`, arrays, `CHECK`, índices parciales, el relleno de 00011 con `jsonb_each_text`). SQLite tendrá su propio juego, un solo fichero con el esquema de ahora, sin repetir la historia: una base de SQLite empieza vacía.
- `store.Open` migra con goose en dialecto Postgres y con el cerrojo de sesión de Postgres (`pg_advisory_lock`), que evita que dos réplicas migren a la vez. Con SQLite, goose en dialecto `sqlite3` y sin ese cerrojo (hay una sola instancia).
- `DATABASE_URL` decide el motor: `postgres://…` como hoy y, por ejemplo, `sqlite:///ruta/intake.db` para SQLite.

## Errores

`db.IsUniqueViolation` y `db.IsForeignKeyViolation` miran hoy el código de Postgres (`23505`, `23503`). Con SQLite, los códigos extendidos `SQLITE_CONSTRAINT_UNIQUE` (o `PRIMARYKEY`) y `SQLITE_CONSTRAINT_FOREIGNKEY`.
