# API

The REST API (`/api/0/`) takes an API token in `Authorization: Bearer <token>` ([API tokens](cli.md#api-tokens)), or the UI's session cookie. Tokens are all equal: with one, you read every error of every project, with what their events carry (users, requests, breadcrumbs), and can comment, assign, change states, edit filters and alert rules and record releases. The [MCP server](agents.md) runs the same queries.

```sh
curl -H "Authorization: Bearer $MOTHLOG_TOKEN" 'https://errors.example.com/api/0/issues/?query=is:open+browser:Safari'
```

Lists return `{"data": [...], "next_cursor": "..."}`: for the next page, pass `cursor=<next_cursor>`; `limit` goes from 1 to 100 (25 by default). Errors are `{"error": "..."}`, with 401 if the token is missing, doesn't exist or was revoked. Responses go with `Cache-Control: no-store`.

## Routes

| Route | What it returns |
|---|---|
| `GET /api/0/projects/` | each project's id, name, DSN and own Linear labels (`linear_label_ids`) |
| `GET /api/0/issues/?project=&status=&query=&sort=&cursor=&limit=&daily=&environment=` | issues, with `user_count`, the distinct affected users; `query` uses the [search language](#search); `status` is `open`, `resolved`, `resolved_next`, `silenced` or `all`, and empty is `open` unless `query` has an `is:`; `sort`, `last_seen` (default), `first_seen` or `count`; `project`, id or name; with `daily=1`, each issue has `daily`, its events per day over the last 14 days as in the detail; with `environment`, only issues with events in that environment, and their events, first and last seen and `daily` are those of that environment (`user_count` is still the total) |
| `GET /api/0/issues/{id}/?environment=` | the issue with its latest event, events per day over the last 14 days, the most frequent values of environment, release, user and `organization_id`, its `first_release`, `last_release` and `fixed_in_release`, and its `links` to Linear and GitHub with their state there; with `environment`, all of it for that environment |
| `GET /api/0/issues/{id}/events/?environment=` | its events, newest first |
| `GET /api/0/issues/{id}/tags/` | `user_count` and, for each tag, the total events that had it, how many distinct values it has, and the 10 most frequent with their count, percentage and last seen |
| `GET /api/0/tags/?project=` | the tag keys there are, with how many issues each appears in; for autocompletion |
| `GET /api/0/tags/{key}/values/?prefix=&project=` | the 20 most frequent values of a key that start with `prefix`, case-insensitively |
| `GET /api/0/issues/{id}/stacktrace.md` | the latest event in Markdown, meant for a model |
| `GET /api/0/issues/{id}/context.md?environment=&event=` | the error's whole context in one Markdown for an agent: the summary with the link to the UI, releases, environments, the most frequent tags, the stack trace with code, breadcrumbs, the request and the user of the latest event (or of `event`, by id or `event_id`), the history and what it can do; over 64 KB, it drops breadcrumbs, library frames and old history, and says so |
| `POST /api/0/issues/{id}/status` | changes the status with `{"status": "open\|resolved\|resolved_next\|silenced"}` and, optionally, `"comment"`, which stays attached to the change in the history; `resolved_next` keeps the release of the last event to arrive (409 if it has none) |
| `GET /api/0/issues/{id}/history/` | the history, oldest first: comments (`type: comment`), status changes (`type: status`, with `source`, `from`, `to`, `release` and the change's `comment`), assignments (`type: assignment`, with `source`, `assignee`, empty when unassigning, and `previous`) and Linear issues created by hand (`type: link`, with `link`); `mine` marks the caller's |
| `POST /api/0/issues/{id}/assignee` | assigns with `{"assignee": "me"}` (the caller: the session's person or the token), a person's login or the name of an active token; `"kind": "user\|token"` is only needed if the name is both; `null` or empty unassigns. Returns the issue with its `assignee` |
| `DELETE /api/0/issues/{id}/assignee` | unassigns it |
| `GET /api/0/assignees/` | who can be assigned: people who have signed in to the UI (`kind: user`, with `display_name` and `avatar_url`) and active tokens (`kind: token`) |
| `POST /api/0/issues/{id}/comments/` | comments with `{"body": "..."}` in Markdown, up to 20,000 characters (201) |
| `DELETE /api/0/issues/{id}/comments/{comment}` | deletes one of your own comments (204; 403 if it is someone else's) |
| `POST /api/0/issues/{id}/linear` | creates the error's Linear issue, linked and with the caller as author in the history (201); if it already has one, returns it (200); 409 without `LINEAR_API_KEY` and 502 if Linear fails |
| `GET /api/0/integrations/` | the URLs of the Linear and GitHub webhooks and, for each, whether the server has its secret (`secret_set`) and its key or token (`token_set`), and when the last delivery with a good signature arrived |
| `GET /api/0/events/{id}/` | the whole normalized event, by id or by `event_id` |
| `GET /api/0/environments/?project=` and `GET /api/0/projects/{id}/environments/` | the environments of the events, with how many issues and events they have and when the last one was seen |
| `GET /api/0/releases/?project=&environment=&cursor=&limit=` and `GET /api/0/projects/{id}/releases/` | releases, newest first, with their deploys, how many new errors (`new_issues`), errors with events (`issues`), regressions and events each brought, and their health (`health` and `health_by_environment`, in [Release health](sdks.md#release-health)); with `environment`, those with events, deploys or sessions in it, counting only those |
| `POST /api/0/projects/{id}/releases/` | creates the release `{"version": "..."}` (201, or 200 if it existed); with `"environment"` (and `"deployed_at"`, optional) it also records a deploy and returns in `resolved_issues` the issues it resolved |
| `GET /api/0/projects/{id}/releases/{version}/?environment=` | the release with its deploys, the previous and next ones, and its issues: `new` (appeared with it), `regressed`, `continued` (already existed and kept happening) and `stopped` (happened in the previous one and not in this one, or were resolved when it was deployed); also its health and `health_series`, its sessions per hour over the last 72 h |
| `GET` and `POST /api/0/projects/{id}/releases/{version}/deploys/` | its deploys; `POST` with `{"environment": "production"}` records one (and creates the release if it didn't exist) |
| `GET /api/0/projects/{id}/filters` | the project's filters, per-issue cap, sampling and per-minute limit |
| `PATCH /api/0/projects/{id}/filters` | changes the fields in the body, with the same names; `null` on a number goes back to its default; 400 with an invalid value |
| `GET /api/0/projects/{id}/dropped?days=` | what was dropped in the last `days` days (30 by default, today included): `total`, `reasons` with each reason, and `daily` per day and reason; `total` is events, without sessions (`session_*`) or stripped fields (`invalid_release`, `invalid_env`) |
| `GET /api/0/projects/{id}/default-alerts` | which default channels the project alerts on without rules (`slack`, `linear`), which ones the server has configured (`configured`) and whether the project has error rules (`has_rules`), with which they don't apply |
| `PATCH /api/0/projects/{id}/default-alerts` | turns on or off the channels in the body, `{"slack": true, "linear": false}`; 400 with another key or a value other than `true` or `false` |
| `GET`, `POST /api/0/projects/{id}/rules` | its alert rules, with secrets masked; creates one (201) |
| `GET`, `PUT`, `DELETE /api/0/projects/{id}/rules/{rule}` | one rule; replaces it whole; deletes it (204) |
| `POST /api/0/projects/{id}/rules/{rule}/test` | with `{"action_id": "..."}`, sends a test alert through that action; 502 with the error if the destination fails |
| `GET`, `PUT /api/0/projects/{id}/digest` | the project's [digest](alerts.md#daily-or-weekly-digest): `enabled`, `frequency` (`daily` or `weekly`), `weekday` (0 Sunday … 6 Saturday), `hour`, `timezone`, `language` (`en` or `es`), `send_empty` and `destinations` (`email`, `slack` or `webhook` actions, with secrets masked), plus `last_sent_at` and `next_send_at`; `PUT` replaces it and keeps the secrets that come masked or empty |
| `GET /api/0/projects/{id}/digest/preview?language=&frequency=` | the digest of the last 24 hours (or 7 days) with the saved settings: `subject`, `html`, `text`, `markdown` and `digest`, the sections |
| `POST /api/0/projects/{id}/digest/send` | sends it right away to each destination and returns in `data` how it went for each (`ok` or `error`) |
| `GET /api/0/monitors/?project=&status=` and `GET /api/0/projects/{id}/monitors/` | cron monitors, failing ones first, with their schedule, state (`pending`, `ok`, `error`, `missed` or `timeout`), `muted`, `last_checkin_at`, `next_checkin_at` (when the next one is expected) and `recent`, their last 30 runs |
| `POST /api/0/projects/{id}/monitors/` | creates one with `{"name": "...", "schedule": {"type": "crontab", "value": "0 3 * * *"}}` (or `{"type": "interval", "value": 2, "unit": "hour"}`) and, optionally, `slug` (otherwise from the name), `timezone`, `checkin_margin`, `max_runtime` (in minutes), `environment` and `muted` (201) |
| `GET`, `PATCH`, `DELETE /api/0/projects/{id}/monitors/{slug}/` | the monitor with the first runs of its history (`checkins`, `next_cursor`); changes the fields in the body, with `{"muted": true}` mutes it; deletes it with its history (204) |
| `GET /api/0/projects/{id}/monitors/{slug}/checkins/?cursor=&limit=` | its history, newest first: state, duration, environment, release, when it was due and when it started and ended (`limit` up to 200, 50 by default) |
| `GET /api/0/uptime-monitors/?project=&status=` and `GET /api/0/projects/{id}/uptime-monitors/` | uptime monitors, down ones first, with their state (`pending`, `up` or `down`; `status=paused` gives the paused ones), `down_since`, `uptime_24h` and `uptime_30d` (the share of time without an incident) and `recent`, their last 30 checks |
| `POST /api/0/projects/{id}/uptime-monitors/` | creates one with `{"name": "...", "url": "https://..."}` and, optionally, `slug`, `method` (`GET` or `HEAD`), `interval_seconds` (30 to 3600, 60 by default), `timeout_seconds` (1 to 60, 10 by default, not more than the interval), `expected_status` (`200-299` by default; `2xx,301` works), `failure_threshold` (1 to 10, 2 by default) and `paused` (201) |
| `GET`, `PATCH`, `DELETE /api/0/projects/{id}/uptime-monitors/{slug}/` | the monitor with its incidents (`incidents`), its last 60 checks (`checks`) and response times over the last 24 h in 15-minute buckets (`response_times`); changes the fields in the body, with `{"paused": true}` pauses it; deletes it with its checks and incidents (204) |

The routes that `sentry-cli` uses are in [Source maps](sdks.md#with-sentry-cli), the ingestion endpoint in [Ingestion](ingestion.md), and `/metrics` in [Metrics](metrics.md).

## Assigning

Each issue can have an assignee: a person or a token, which is how an agent is assigned. People are saved when they sign in to the UI (login, name, avatar and the last time they signed in) so they can be picked; nobody but those who sign in to the UI or have a token sees that data. An agent assigns itself an error with `assign_issue` and `{"issue_id": 42, "assignee": "me"}` (or `POST /api/0/issues/42/assignee` with `{"assignee": "me"}`) and finds its own with `assigned:me`; a person, with the picker on the error's page or by assigning it the token's name. If the token is revoked, the assignment keeps the name and shows as inactive (`"active": false`). It shows in the issue (`assignee`), in the list, in `get_issue`, `list_issues` and the `context.md`.

## History

Each issue keeps its comments (Markdown, with the author and whether it is a person or a token), its assignments and unassignments (who, to whom and from where) and its status changes: who, when, from which state to which, the release if there is one, and from where (`ui`, `api`, `mcp`, `ingest` when reopened as a regression, `deploy` when resolved by a deploy, `linear` or `github` from a [linked issue](alerts.md#linear-and-github-both-ways), and `import`). The author is the session's user in the UI, the token's name in the API and the MCP, or the person with the client they acted through (`via`) when the MCP is used with OAuth. Repeating the same status records nothing; a comment that comes with it is kept on its own. It is deleted with the issue.

## Search

The `query` of the issue list (in the API, in the MCP's `list_issues` and in the UI's search bar) is a list of space-separated terms that must all hold:

| Term | What it finds |
|---|---|
| `is:open`, `is:resolved`, `is:resolved_next`, `is:silenced` | the status |
| `is:regressed` | open issues that came back after being resolved |
| `release:1.4.0`, `environment:production`, `level:warning` | issues with some event of that release, environment or level |
| `project:web` | the project, by name or id |
| `assigned:me`, `assigned:ana`, `assigned:backend-agent` | assigned to the searcher (the session's person or the token), to a person by login or to a token by name, case-insensitively |
| `is:assigned`, `is:unassigned` | with or without an assignee; they don't change the default status (`is:unassigned` still searches open issues) |
| `user:ana@example.com` | issues that happened to that user, by id, email (case-insensitive), username or IP; it needs the exact value, because users are stored hashed |
| `browser:Safari`, `os:"Mac OS"`, `device:iPhone`, `runtime:CPython`, `component:api` | any tag, with its exact value: those from `contexts` and the user agent (`browser`, `os`, `device`, `runtime`) and the SDK's `tags` |
| `!browser:Chrome`, `!is:resolved`, `!assigned:me`, `!timeout` | `!` in front negates: issues without any event with that value, or without that text |
| `timeout`, `"division by zero"` | text in the title or the culprit, case-insensitively; quotes group spaces (`\"` and `\\` inside) |

Examples:

```text
is:open browser:Safari                    # open, happening on Safari
is:regressed release:1.4.0                # reopened, happening again in 1.4.0
project:web os:"Mac OS" !environment:staging
user:ana@example.com is:open              # what is failing for one person
assigned:me                               # mine, open
is:unassigned environment:production      # what nobody has in production
"Cannot read properties" level:error
```

A key is a letter or `_` followed by letters, digits, `_`, `.` or `-`; anything without that shape before the colon (a URL, `12:30`) or with nothing after it is text, so a title pasted as is, like `TypeError: Cannot read properties`, searches that text. A malformed query gets a 400 with `query_error`: `code` (`unclosed_quote`, `empty_key`, `lone_negation`, `bad_status` or `after_quote`), `position` (the character, from 0) and, when relevant, `key` and `value`; the `error` message says the same in words.
