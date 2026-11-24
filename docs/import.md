# Import from Sentry

To avoid starting from scratch when coming from Sentry (sentry.io or a self-hosted one), `mothlog import sentry` brings through its API the projects, the issues with their latest events, and the releases with their deploys:

```sh
export SENTRY_IMPORT_TOKEN=...   # an organization token with read access (org:read, project:read, event:read)
mothlog import sentry --url https://sentry.io --org my-org --dry-run
mothlog import sentry --url https://sentry.io --org my-org [--project api --project web] [--since 2026-06-01] [--events 5]
```

The token only goes in `SENTRY_IMPORT_TOKEN`, never on the command line, and is only sent in the `Authorization` header to the server in `--url`: if an API page points to another server, it isn't followed. It doesn't show in the progress, the report or the errors.

What comes:

- **Projects**: all of the organization's, or those in `--project` (by slug, can be repeated). One that doesn't exist here is created with the same name; if there is already one with that name, it is used as it is. The new DSN is in `mothlog projects list`.
- **Releases** of each project, with their creation and release dates, and their **deploys** per environment. Imported deploys resolve nothing: what they resolved there already comes in each issue's state.
- **Issues**, resolved and ignored ones too: title, culprit, type, level, count, users, first and last seen and status. `unresolved` is open, `ignored` is silenced and `resolved` is resolved; resolved in a release keeps that release (an event from it or a later one reopens it), and resolved in the next release, that of the last event. The status change goes in the history with the source `import`.
- **Events**: the latest `--events` of each issue (5 if not given), turned into the SDK's shape and read as ingestion does, so the detail, the stack trace with code, the breadcrumbs, the tags and the `context.md` come out as with an event received.

`--since` leaves out issues last seen and releases created before that date (`2026-06-01` or `2026-06-01T10:00:00Z`). An issue for which Sentry no longer keeps any event is skipped, because without events retention would delete it. Since imported events count from when they are imported, they stay `RETENTION_DAYS` days even if they are older.

The API's rate limit is respected: on a 429 it waits for what `Retry-After` (or `X-Sentry-Rate-Limit-Reset`) says, and if `X-Sentry-Rate-Limit-Remaining` reaches 0, until the limit resets. It paginates with the `Link` header, and the progress goes to standard error.

**Running it again** duplicates nothing: for each project, issue, release and deploy, Mothlog keeps where it came from (server, organization and id in Sentry, in `import_origins`) and what it brought. When repeated, only what changed there since last time changes: the title, level or status only if they changed in Sentry, so as not to overwrite what was changed here; the count adds what grew there, because here it may also carry events from ingestion. Events that were already here stay as they are.

**New events are grouped with what was imported**: each imported issue's fingerprint is taken from its latest event as ingestion would, so a new event of the same error goes to it and reopens it or counts like any other. That isn't possible when there is already another issue here with that fingerprint (for example, if the project was already receiving events), and then the report says so. It won't match either if the SDK sets its own `fingerprint`, which Sentry's API doesn't return, nor for browser errors if the source maps aren't uploaded here, because Sentry returns the stack trace already translated.

`--dry-run` does the same without writing anything (reads go in read-only transactions) and says what it would create and update. At the end, the report counts the projects, releases, deploys, issues and events created, updated, unchanged and skipped, and lists the issues that don't group, the skipped ones and the API errors, which don't stop the import.

What doesn't come: attachments, replays, transactions and performance, profiles, the comment and activity history, assignees, alert rules, cron and uptime monitors, source maps, and events beyond the latest `--events` (the count is Sentry's).

To try it by hand from a copy of the repository, `SENTRY_IMPORT_TOKEN=any go run ./tools/sentry-falso` serves a fake Sentry API on `http://127.0.0.1:7803` with the responses in `testdata/sentry/` (organization `ejemplo`).
