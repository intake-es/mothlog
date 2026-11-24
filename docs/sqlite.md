# SQLite: lo que es propio de Postgres y cómo se hace

Mothlog corre con Postgres 16 o con SQLite (un solo binario, sin servidor de base de datos). El acceso a la base pasa por `internal/db`, que tiene una implementación para cada motor (`postgres.go` y `sqlite.go`), y el SQL que cambia de un motor a otro está localizado. Esta página dice qué es propio de Postgres, dónde está y cómo se hace con SQLite. Cómo instalarlo y sus límites, en el README («Con SQLite, sin servidor de base de datos»).

## Con SQLite, una sola instancia

SQLite admite un solo escritor a la vez y el fichero vive en una máquina, así que **con SQLite el servicio corre en una sola instancia**: nada de réplicas detrás de un balanceador. Varias piezas cuentan con varias réplicas en Postgres y se apoyan en `FOR UPDATE SKIP LOCKED` para no pisarse (el envío de avisos, la sincronización con Linear y GitHub, los barridos de crons, las comprobaciones de disponibilidad, la retención, la conversión de eventos antiguos y el resumen). Con una sola instancia y un solo escritor, esos cerrojos sobran.

Cómo está hecho (`internal/db/sqlite.go`, con `modernc.org/sqlite`, Go puro: el binario sigue con `CGO_ENABLED=0`):

- `DATABASE_URL=sqlite:///ruta/mothlog.db` (o `sqlite://./mothlog.db`, relativa) elige SQLite; `postgres://…` sigue como antes.
- Dos `*sql.DB` sobre el mismo fichero: uno de escritura con **una sola conexión**, cuyas transacciones empiezan con `BEGIN IMMEDIATE` (cogen el cerrojo de escritura al empezar: lo que en Postgres hace `FOR UPDATE` lo da ese cerrojo), y otro de lectura con varias conexiones en `query_only`. Las consultas sueltas que solo leen (`SELECT`, `VALUES` o `WITH` sin escrituras) van a las de lectura; el resto, a la de escritura. Las transacciones de solo lectura (`TxOptions{ReadOnly: true}`) van a una de lectura.
- Al abrir: `journal_mode=WAL`, `busy_timeout=10000`, `foreign_keys=ON` y `synchronous=NORMAL`.
- El arranque del servicio (`store.Open`) coge un cerrojo de fichero (`flock` sobre `<fichero>.lock`) y se niega si lo tiene otro proceso, con un mensaje que lo dice. Las órdenes de la línea de comandos (`store.OpenForCommand`: `projects`, `tokens`, `releases`, `import`) no lo cogen, para que puedan correr con el servicio arrancado, como con Postgres.
- Como en Postgres, un error de la base deja la transacción abortada: lo siguiente falla y `Commit` la deshace, hasta volver a un savepoint anterior. SQLite por sí solo deshace solo la sentencia que falla y deja seguir; el código cuenta con el comportamiento de Postgres (un aviso que no se pudo apuntar no debe quedar a medias).
- `Begin` dentro de una transacción abre un savepoint, como con pgx. Un `SendBatch` fuera de una transacción va en una, como hace Postgres con un lote.

## El dialecto: cada función, dónde se usa y cómo es en SQLite

`db.Dialect` (`internal/db/dialect.go`) da los trozos de SQL que se escriben distinto en cada motor; `db.SQLiteDialect` está en `internal/db/dialect_sqlite.go`. Cada consulta lo pide a su `Querier` (`tx.Dialect()`, `s.pool.Dialect()`). `TestPostgresDialect` y `TestSQLiteDialect` comprueban lo que escribe cada uno. Las rutas son relativas a `internal/`; entre paréntesis, la función.

| Función | Postgres | Dónde | SQLite |
|---|---|---|---|
| `ForUpdate(of...)` | `FOR UPDATE [OF t]` | `ingest/batch.go` (readBatch); `ingest/ingest.go` (lockIssue); `ingest/sessions.go` (IngestSession); `store/assign.go` (AssignIssue); `store/checkins.go` (saveCheckIn); `store/history.go` (changeStatusTx); `store/imports.go` (ImportIssue); `store/links.go` (ApplyExternalStatus); `store/monitors.go` (lockMonitor); `store/releases.go` (AddDeploy); `store/uptime.go` (UpdateUptimeMonitor, RecordUptimeCheck) | Cadena vacía: la transacción ya tiene el cerrojo de escritura (`BEGIN IMMEDIATE`). |
| `ForUpdateSkipLocked(of...)` | `FOR UPDATE [OF t] SKIP LOCKED` | `alert/alert.go` (sendOne); `alert/sync.go` (syncOne); `ingest/retention.go` (purgeEmptyIssues); `store/checkins.go` (sweepTimeout, sweepMissed); `store/convert.go` (convertBatch); `store/digests.go` (EnqueueDigest); `store/uptime.go` (ClaimUptimeChecks) | Cadena vacía: con una sola instancia no hay otra réplica de la que saltarse filas. |
| `InList(expr, $n)` | `expr = ANY($n)` con un slice de Go | `alert/alert.go`, `alert/rules.go`, `ingest/retention.go`, `store/bundles.go`, `store/health.go`, `store/links.go`, `store/releases.go`, `store/uptime.go` | `expr IN (SELECT value FROM json_each($n))`; la implementación de `db` pasa los slices de los argumentos (`[]int64`, `[]string`…; `[]byte` sigue siendo un BLOB) a un texto JSON. |
| `InBytes(expr, $n)` | `expr = ANY($n)` | `store/issues.go` (termSQL, `user:`) | `expr IN (SELECT unhex(value) FROM json_each($n))`: un `[][]byte` va en JSON con cada elemento en hexadecimal. Va aparte de `InList` porque el dialecto no sabe el tipo de los elementos. |
| `Unnest(alias, $n, "col tipo"...)` | `unnest($1::bigint[], $2::text[]) AS k(a, b)` | `ingest/batch.go`, `ingest/tags.go`, `store/convert.go`, `store/eventdata.go`, `store/filters.go`, `store/issues.go`, `store/monitors.go`, `store/releases.go`, `store/uptime.go` | `(SELECT j.value AS a, ($2 ->> j.key) AS b FROM json_each($1) j) AS k`: recorre la primera lista y toma de las demás el elemento de la misma posición con `->>`; las columnas `bytea` van con `unhex(…)`. Unir los `json_each` por `key`, como decía el plan, es un producto de n×n filas sin índice: con lotes de cientos de eventos se nota. |
| `UnnestNumbered(alias, num, …)` | `unnest(…) WITH ORDINALITY AS e(…, n)` | `store/eventdata.go` (InsertEventsQuery) | La misma subconsulta con `j.key + 1 AS n`. |
| `Greatest`, `Least` | `GREATEST(a, b)`, `LEAST(a, b)` | `ingest/batch.go`, `ingest/ingest.go`, `ingest/sessions.go`, `ingest/tags.go`, `store/digest_data.go`, `store/imports.go`, `store/releases.go`, `store/uptime.go` | `max(COALESCE(a, b), COALESCE(b, a))` y `min(…)`: siempre con los `COALESCE`, porque en SQLite dan NULL si un argumento es NULL y en Postgres lo ignoran. |
| `ILike(expr, p)` | `expr ILIKE p` | `store/issues.go` (termSQL); `store/tags.go` (TagValues) | `lower(expr) LIKE lower(p) ESCAPE '\'`, con un `lower` propio que entiende Unicode (registrado en el driver). El `ESCAPE` hace falta porque los patrones escapan `%` y `_` con `\` y SQLite no tiene carácter de escape por defecto. |
| `SecondsBetween(to, from)` | `EXTRACT(EPOCH FROM to - from)` | `store/digest_data.go`, `store/metrics.go`, `store/uptime.go` | `(CAST(to - from AS REAL) / 1000000)`, con las fechas en microsegundos. |
| `AddSeconds`, `SubSeconds`, `AddMinutes` | `ts ± make_interval(secs => n)` | `store/checkins.go`, `store/links.go`, `store/uptime.go` | `ts ± CAST(round(n * 1000000) AS INTEGER)` (o `60000000` por minuto). |
| `JSONText`, `JSONType` | `doc->>'k'`, `jsonb_typeof(doc->'k')` | `store/rules.go` (IssueRulesSQL) | `json_extract(doc, '$.k')` y `json_type(doc, '$.k')`. |
| `Collate(expr)` | `expr` | Los `ORDER BY` de texto de `store` (etiquetas, entornos, nombres, slugs, tokens…) | `expr COLLATE en_us`: ver «Orden del texto». |
| `Typed(expr, tipo)` | `expr::tipo` | `alert/sync.go`, `ingest/batch.go`, `store/imports.go`, `store/issues.go`, `store/uptime.go` | `expr`: el tipo solo se lo dice a Postgres. |

`db.ByEngine(d, postgres, sqlite)` elige entre dos consultas enteras cuando no basta con trozos.

## Lo propio de Postgres que sigue en las consultas

Cada una tiene al lado su versión de SQLite, con `db.ByEngine` o con `s.sqlite()`.

| Construcción | Dónde | SQLite |
|---|---|---|
| `LATERAL` | `alert/alert.go` (loadIssue); `store/digest_data.go` (DigestRegressions); `store/issues.go` (issueSQL, IssueDetailIn); `store/monitors.go` (ListMonitors); `store/releases.go` (releaseFrom, ReleaseDetail ×2); `store/uptime.go` (ListUptimeMonitors) | Los agregados por fila de `issueSQL`, `releaseFrom` y el de las que dejaron de salir de `ReleaseDetail` van en un `JOIN` con una subconsulta sin agregar (`SELECT x.id, (SELECT sum(…) …) AS n … FROM tabla x`), que SQLite aplana y calcula fila a fila con el índice, con los mismos nombres de columna que la de Postgres; los issues de una release de `ReleaseDetail`, en un `JOIN` con los recuentos de la release agrupados por issue. «La última fila de cada uno» (`loadIssue`, `DigestRegressions`) pasa a una subconsulta escalar con `max(id)` o `ORDER BY … LIMIT 1`, con un `EXISTS` donde el `CROSS JOIN LATERAL` dejaba fuera a los que no tienen. «Las N últimas de cada monitor» (`ListMonitors`, `ListUptimeMonitors`) pasa a `c.id IN (SELECT id … WHERE monitor_id = m.id ORDER BY … LIMIT N)`: lee solo esas N por el índice, mientras que `ROW_NUMBER() OVER (PARTITION BY …)`, como decía el plan, recorre todas las de cada monitor. El `CROSS JOIN LATERAL (VALUES …)` de `IssueDetailIn` pasa a un `UNION ALL` de las cuatro columnas. |
| `generate_series`, `to_char`, `AT TIME ZONE 'UTC'`, `::date`, `interval '1 day'` | `store/issues.go` (DailyCountsIn); `store/filters.go` (AddDropped, DroppedSince) | Los días con un CTE recursivo en microsegundos de sus 00:00 UTC (`($2 / 86400000000 - n + 1) * 86400000000` y `+ 86400000000`); su texto con `strftime('%Y-%m-%d', día / 1000000, 'unixepoch')`. `dropped_events.day` guarda el entero de las 00:00 UTC. |
| `date_bin($3::interval, checked_at, $2)` | `store/uptime.go` (UptimeDetail) | `$2 + (checked_at - $2) / $3 * $3`, con el paso en microsegundos. |
| `GROUPING SETS` | `store/health.go` (loadHealth) | Dos consultas unidas con `UNION ALL`: por release y entorno, y por release. |
| `bool_or` | `store/releases.go` (ReleaseDetail) | `max(expr)`. |
| `INSERT` dentro de un `WITH` | `ingest/tags.go` (recordTags); `ingest/batch.go` (tagsQueries) | SQLite no admite escrituras en un CTE: van en sentencias seguidas, en la misma transacción con el cerrojo de escritura. Para contar los usuarios nuevos, primero se suman los que no estaban y luego se guardan. No estaba en el inventario. |
| `UPDATE t a`, `DELETE FROM t a` | `ingest/batch.go`, `ingest/retention.go`, `store/monitors.go`, `store/uptime.go` | SQLite necesita `AS` en el alias (`UPDATE issues AS i`), que Postgres también acepta: se escribe así en los dos. En un `UPDATE … FROM … RETURNING`, SQLite no deja nombrar la tabla en el `RETURNING`: `ClaimUptimeChecks` llama `due_id` a la columna de la subconsulta para que `RETURNING id, …` no sea ambiguo en Postgres. |
| `INSERT … SELECT … FROM x ON CONFLICT` | `ingest/batch.go`, `ingest/tags.go`, `store/releases.go` (TrackEventsQuery) | Lleva un `WHERE true` antes del `ON CONFLICT`, que SQLite necesita para no leer el `ON` como la condición de un `JOIN`; Postgres lo acepta igual. |
| `::uuid`, `replace(CAST(uuid AS text), '-', '')` | `alert/alert.go`, `alert/cron.go`, `ingest/batch.go`, `store/issues.go`, `store/monitors.go` | Los uuid se guardan como texto, tal como los da Go: `events.event_id` y `monitor_checkins.check_in_id` en 32 hexadecimales en minúscula, `sourcemaps.debug_id` con guiones (así lo normaliza `internal/sourcemap`) y `alert_outbox.linear_create_id` con guiones, porque se lee con `CAST(… AS text)` y se manda a Linear como UUID. El `replace(CAST(… AS text), '-', '')` vale tal cual. `Event` pasa la referencia a 32 hexadecimales en minúscula antes de buscar. El `gen_random_uuid()` del `DEFAULT` de `linear_create_id` es en SQLite una expresión con `randomblob` que da un UUID v4. |
| `now()` | Casi todas las escrituras | `now()` es una función registrada en el driver que da la hora en microsegundos. A diferencia de Postgres, no es la misma en toda la transacción, sino la del momento. Los `DEFAULT (now())` del esquema la usan. |
| JSON de los eventos antiguos: `e.data->'user'->>'id'`, `jsonb_array_length`, `btrim`, `concat_ws` | `store/digest_data.go` (legacyUserIdent); `store/issues.go` (legacyUserLabel, legacyOrganizationID, legacySummary) | Una base de SQLite nace con todos los eventos comprimidos: con SQLite esas expresiones son `NULL` (`legacy(d, …)`), y `ConvertLegacyEvents` y `RunEventConversion` no hacen nada. Por eso no hace falta registrar `btrim` ni `concat_ws`. |
| Arrays como columnas (`text[]`) | `projects.linear_label_ids`; `project_filters.*` | Texto JSON. La implementación de `db` escribe un `[]string` como array JSON y lo lee de vuelta. `Filters` ya no usa `COALESCE(…, '{}')`: con un proyecto sin filtros deja las listas vacías en Go, igual con los dos motores. |
| `jsonb` como columnas | `alert_rules.conditions`, `actions`; `alert_outbox.detail`; `project_digests.destinations`; `import_origins.imported` | Texto JSON: los structs y mapas se escriben con `json.Marshal` y se leen con `json.Unmarshal`. El JSON que ya viene en bytes tiene que ir como `json.RawMessage` (que va como texto), no como `[]byte` (que es un BLOB): así lo pasan ahora `enqueueRule`, `EnqueueDigest` y `saveOrigin`; Postgres lo guarda igual. |
| `RETURNING`, `ON CONFLICT … DO UPDATE/NOTHING` (también con índice parcial), `FILTER (WHERE …)`, `count(*) OVER ()`, `IS DISTINCT FROM`, comparar filas `(a, b) < ($1, $2)` | Por todo `store`, `alert` e `ingest` | Valen en SQLite tal cual (el driver trae SQLite 3.54). |

## Fechas y tipos

Las fechas se guardan como **enteros de microsegundos desde 1970 en UTC**: se comparan y ordenan como números, restar da microsegundos y no se pierde precisión respecto a Postgres. La implementación de `db` (`internal/db/sqlite_convert.go`) convierte los argumentos (`time.Time`, slices, structs, mapas, punteros, tipos con nombre) y lo que se lee (`*time.Time`, `**time.Time`, `*[]string`, structs JSON, `*bool` desde 0 y 1…), así que `store` sigue trabajando con `time.Time`, `[]string` y sus structs. Las fechas leídas van en la zona local, como las da pgx.

Las tablas son `STRICT`: SQLite se niega a guardar un texto en una columna de fechas o un BLOB en una de JSON, en vez de guardarlo y comparar mal después. Así salieron un SQL de un test que ponía una fecha como texto y los tres JSON que iban como `[]byte`.

## Orden del texto

Postgres ordena el texto con la colación de la base (`en_US.UTF-8` en la imagen `postgres:16`): sin tener en cuenta la puntuación ni los espacios al principio, con mayúsculas y tildes después; SQLite, por bytes. Para que la API, el MCP, los Markdown y el resumen salgan iguales con los dos motores, el driver registra la colación `en_us` (`internal/db/sqlite_collate.go`): compara las letras y los números con `golang.org/x/text/collate` (inglés), desempata por la puntuación en el orden de glibc y, al final, por bytes, como Postgres. `TestCollateLikePostgres` comprueba el orden que da Postgres 16 con una lista de textos. Solo se usa en los `ORDER BY` (`Dialect.Collate`), no en las columnas, para que no cueste en cada índice y `sqlite3` pueda copiar la base sin conocerla.

## Migraciones y arranque

- Las migraciones de `migrations/` son de Postgres. SQLite tiene las suyas en `migrations/sqlite/`: un solo fichero, `00022_esquema.sql`, con el esquema de ahora, sin repetir la historia. Lleva el número de la última de Postgres para que una migración nueva tenga el mismo número en los dos motores. Añade índices que Postgres no tiene sobre algunas claves ajenas (`alert_outbox.issue_id`, `rule_id`, `waits_for`, `issue_links.rule_id`, `issue_link_sync.link_id`), porque SQLite busca las filas que referencian a una que se borra.
- `store.Open` migra con goose en dialecto `sqlite3` y sin cerrojo (hay una sola instancia); con Postgres, como siempre, con `pg_advisory_lock`.

## Errores

`db.IsUniqueViolation` y `db.IsForeignKeyViolation` miran el código de Postgres (`23505`, `23503`) o los códigos extendidos de SQLite (`SQLITE_CONSTRAINT_UNIQUE` o `PRIMARYKEY`, y `SQLITE_CONSTRAINT_FOREIGNKEY`).

## Los tests

`storetest` usa Postgres si hay `MOTHLOG_TEST_DATABASE_URL` y, si no, SQLite en un fichero temporal. Con SQLite se saltan, diciendo por qué, los que solo tienen sentido con Postgres: las migraciones de Postgres sobre datos de antes, la conversión y lectura de los eventos guardados en jsonb, varias réplicas a la vez con `SKIP LOCKED`, la purga que se salta un issue bloqueado por otra transacción (con SQLite, `TestPurgeKeepsEventBeingIngested` comprueba que la purga espera y el evento no se pierde) y un evento con el carácter NUL, que Postgres rechaza y SQLite guarda.
