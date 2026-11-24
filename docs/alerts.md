# Alerts

Alerts are written in the language of `DEFAULT_LANGUAGE` ([Language](configuration.md#language)).

## Default alerts

A project without [alert rules](#alert-rules) alerts like this: a new error, or a regression (an event that reopens a resolved issue), alerts on each channel configured with environment variables. Another event of an open issue doesn't alert. A cron that fails, doesn't arrive or runs too long also alerts on those channels ([Cron monitors](monitors.md#cron-monitors)), and so does an uptime monitor when it goes down and when it comes back ([Uptime monitors](monitors.md#uptime-monitors)).

- **Slack** (`SLACK_WEBHOOK_URL`): a Block Kit message with the kind of alert, the project and the environment (and, for a regression, when it was resolved); the error; the failing line (the own frame closest to the error in the last event, with its code); the release, the space (`organization_name` or `organization_id`), the number of events for a regression and the user id or the request without its query string; and buttons to `<PUBLIC_URL>/issues/<id>` and to the Linear issue. With Linear, the Slack alert waits up to 2 minutes for the Linear one to be sent, to carry its button.
- **Linear** (`LINEAR_API_KEY` and `LINEAR_TEAM_ID`): for a new error, an issue `[<project>] <title>` in `LINEAR_TEAM_ID` with the labels of `LINEAR_LABEL_IDS` plus the project's own, without repeats; its description is the link to the UI and the `stacktrace.md` Markdown. For a regression, that same issue goes back to the team's first `unstarted` state (whatever its name), gets the project's labels it lacks and a comment with the release and the date; if it was deleted in Linear, another one is created. There is one Linear issue per error.

Each default channel can be turned off per project (Settings → Alerts → Default alerts, or `PATCH /api/0/projects/{id}/default-alerts` with `{"linear": false}`); they start on. When off, the project's new errors and regressions don't alert through it, and if Linear is off and Slack isn't, the Slack alert goes out without waiting for Linear and without its button. It only matters while the project has no error rules: with them, it alerts where they say, Linear included. Crons and uptime monitors keep alerting on every configured channel.

An error without a Linear issue can be sent by hand with "Create in Linear" on its page, or with `POST /api/0/issues/{id}/linear`: it creates the issue like the alert does (team, labels and description) and links it, so their states sync the same way, and the history says who created it. If it already has one, it doesn't create another.

### Delivery

Ingestion leaves alerts in `alert_outbox` within its own transaction, so an event that isn't saved doesn't alert, and if queuing fails, the event is saved anyway. Every 5 seconds, what is pending is sent; each replica takes rows with `FOR UPDATE SKIP LOCKED` and sets them aside for 5 minutes before sending them, so no replica sends what another is sending, and alerts of one issue and channel go out in order. The transaction closes before calling the destination and the result is written in another one, so a slow destination doesn't hold up database writes; if the process dies halfway, the alert goes out again after those 5 minutes. If a send fails, it is retried after 1 min, 5 min, 30 min, 2 h, 6 h, 12 h and then every 24 h; after 10 attempts it is abandoned, with the last error in `last_error`.

## Alert rules

With rules, the project stops alerting as above and alerts on what its rules say, where they say; if they are all disabled, it doesn't alert. Errors, crons and uptime monitors are separate: rules only for crons don't remove the alerts of errors or uptime, and the other way round. Rules are edited in the UI (Settings → Alerts), with the API or with the command line. A rule fires when any of its conditions and all of its filters match:

- **Conditions**: a new error (`new`), a regression (`regressed`), more than N events within a window of up to 24 h (`frequency`, counting the events not kept because of the cap or the sampling too) and more than N affected users (`users`). Frequency counts, per rule, only the events that pass its filters; users, as explained below. Both alert once per crossing: the first time they can (outside the rule's throttle) while above the threshold; frequency alerts again when it drops below the threshold and crosses it again. A silenced issue doesn't alert.
- **Crons** (`cron`): a cron that fails (`failed`), doesn't arrive on time (`missed`), runs past its maximum runtime (`timeout`) or works again after failing (`recovered`), from the monitors in `monitors` (their slugs) or, empty, from all of the project's. They alert on a change of state, not on every run that keeps failing, and a muted monitor doesn't alert; of the filters, only the environment counts, and the throttle doesn't apply.
- **Uptime** (`uptime`): an uptime monitor that goes down (`down`) or comes back (`up`, with how long the incident lasted), from the monitors in `monitors` (their slugs) or, empty, from all of the project's. They alert once per incident, when it opens and when it closes; filters and throttle don't apply.
- **Filters**: environments, levels (`fatal`, `error`, `warning`, `info`, `debug`) and tags, with a value or just present.
- **Actions**: Slack (its webhook), Linear (team and labels; without them, `LINEAR_TEAM_ID` and `LINEAR_LABEL_IDS`; needs `LINEAR_API_KEY`), email (`SMTP_URL`), Discord (its webhook), Telegram (bot token and chat), ntfy (topic URL and, if it asks for one, a token), the generic webhook and GitHub (`repo` as `owner/repo`, `labels` and, if `GITHUB_TOKEN` won't do, its own `token`): one issue per error in the repository with the error's `context.md` and the labels, reopened on a regression and commented on for other alerts. Slack and Linear alerts are like the default ones; in Linear, frequency and users comment on the issue without changing its state.
- **Throttle**: `throttle_minutes`, the least time between two alerts of the same rule for the same issue.

`users`, in a rule that doesn't filter by environment, level or tag, uses the issue's affected users (`user_count`), including those from before the rule existed. With filters, it counts on its own the distinct users (id, email, name or IP) of the events that pass them, from when the rule exists.

Everything goes out through `alert_outbox`, with the same retries. Each replica remembers for 30 seconds whether a project has rules, so as not to look it up with every event: what is saved through that replica's API applies at once, and what comes from another replica or the command line, within 30 seconds. "Send test" in the rule editor sends, right away and without going through the outbox, a test alert through one action of the saved rule. Webhook URLs, ntfy topic URLs and tokens aren't returned whole: when editing, leaving them as they come (or empty) keeps them. The generic webhook's secret, if not set, is generated and returned whole only when saving.

```sh
mothlog projects rules backend                          # lists the rules
mothlog projects rules backend show <id>                # one, as JSON, with its secrets masked
mothlog projects rules backend create rule.json         # or - to read it from stdin
mothlog projects rules backend edit <id> rule.json
mothlog projects rules backend test <id> [<action>]     # a test alert through one action, or through all
mothlog projects rules backend delete <id>
```

```json
{
  "name": "Payments in production",
  "conditions": {"new": true, "regressed": true, "frequency": {"events": 100, "window_minutes": 10},
                 "environments": ["production"], "tags": [{"key": "area", "value": "payments"}]},
  "actions": [{"type": "webhook", "url": "https://agent.example.com/alerts"},
              {"type": "email", "to": ["oncall@example.com"]}],
  "throttle_minutes": 30
}
```

### Where alerts can go

The URLs of Slack, Discord, ntfy and the generic webhook, in rules and in the digest, are set by whoever edits the project, but the service requests them from its own network. Like [uptime monitors](monitors.md#uptime-monitors), they never go to cloud metadata, link-local or reserved addresses (also when DNS resolves there: the address is checked right before connecting), and to private networks and loopback only with `ALERTS_ALLOW_PRIVATE=1`. A URL that already points to a metadata address is rejected when saved. Redirects aren't followed (a 3xx is a failure) and the proxy from the environment variables isn't used. Only the destination's status code is kept and returned, never the body; a connection failure is reported without details, which go to the server log. `SLACK_WEBHOOK_URL`, Linear, GitHub, Telegram and email are set by whoever deploys and don't go through this check.

If the URL of an ntfy or webhook action changes, its secrets aren't kept: the ntfy token has to be typed again (a masked one is rejected) and the webhook, without a secret, gets a new one.

### The generic webhook

A `POST` with `Content-Type: application/json`:

```json
{
  "type": "new",
  "delivery": "outbox-812",
  "sent_at": "2026-11-21T10:00:00Z",
  "rule": {"id": 3, "name": "Payments in production"},
  "project": "backend",
  "issue": {"id": 42, "title": "TypeError: …", "culprit": "…", "level": "error", "status": "open", "count": 1,
            "first_seen": "…", "last_seen": "…", "url": "https://errors.example.com/issues/42"},
  "event": {"event_id": "…", "timestamp": "…", "level": "error", "release": "…", "environment": "production",
            "error": "TypeError: …", "location": "app/payments.py:88 in charge", "user": "…", "request": "POST /payments", "tags": {}},
  "trigger": {"events": 100, "window_minutes": 10},
  "markdown": "# TypeError: …"
}
```

- `type` is `new`, `regressed`, `frequency`, `users` or `test`.
- For crons it is `cron_failed`, `cron_missed`, `cron_timeout` or `cron_recovered`: those carry no `issue` or `event` but `monitor` (id, slug, name, schedule, timezone, margin, maximum runtime and `url`) and `check_in` (`check_in_id`, status, `duration_ms`, environment, release, `expected_at` and `started_at`), and `markdown` is a summary of the cron.
- For uptime it is `uptime_down` or `uptime_up`, with `uptime` (id, slug, name, `check_url`, `method`, and `url`, the link to the UI) and `incident` (id, `started_at`, `ended_at` and `duration_ms`, empty while it is down, and `first_error`).
- `trigger` only comes with `frequency` and `users`, and `resolved_at` with regressions.
- `markdown` is the `stacktrace.md`, ready for an agent.
- `delivery` is the same in the retries of one alert.

It comes with `X-Mothlog-Event` (the type), `X-Mothlog-Delivery` and `X-Mothlog-Signature: t=<Unix seconds>,v1=<signature>`, where the signature is the hex HMAC-SHA256, with the action's secret, of `<t>.<body>`. For now the headers from before the rename to Mothlog come too, with the same values (`X-Intake-Errors-Event`, `X-Intake-Errors-Delivery` and `X-Intake-Errors-Signature`); they go away in 2.0.0. To check it, with the body exactly as it arrived, without serializing it again:

```python
import hashlib, hmac, time

def valid_signature(secret: str, header: str, body: bytes, tolerance: int = 300) -> bool:
    parts = dict(p.split("=", 1) for p in header.split(","))
    t, v1 = parts.get("t", ""), parts.get("v1", "")
    if not t.isdigit() or abs(time.time() - int(t)) > tolerance:
        return False  # no timestamp, or too old: it may be a replay
    expected = hmac.new(secret.encode(), f"{t}.".encode() + body, hashlib.sha256).hexdigest()
    return hmac.compare_digest(expected, v1)
```

A 2xx means delivered; any other answer, or none within 15 seconds, means failed, and it is retried.

## Linear and GitHub both ways

Each error keeps its links (`issue_links`): the Linear issue of the alerts, the GitHub issues of the rules and the pull requests that fix it, with their identifier, URL and state there. They show on the error's page in the UI, in `GET /api/0/issues/<id>/` and `get_issue` (`links`) and in the `context.md`.

- **From there to here**: a linked issue that moves in Linear to a `completed` state resolves the error; to a `canceled` one, silences it; and one that leaves either for an open state reopens it (moving from "Todo" to "In Progress" changes nothing). In GitHub, closing the issue resolves it (closing it as "not planned" silences it) and reopening it reopens it. The change goes in the history with the source `linear` or `github` and who made it there.
- **A merged pull request** whose title or description has the error's URL in the UI (`<PUBLIC_URL>/issues/<id>`, alone or after `Fixes`) leaves it "resolved in the next release" (or resolved, if its last event has no release) and gets linked; its Linear issue and its GitHub issues are closed as if it had been resolved here.
- **From here to there**: resolving or silencing an error moves its Linear issue to its team's first `completed` or `canceled` state, and reopening it (by hand or by a regression) takes it back to the first `unstarted` one; in GitHub, resolving it closes the issue, silencing it closes it as "not planned" and reopening it reopens it. These go through `issue_link_sync`, with the same retries as alerts, and use `LINEAR_API_KEY` and the token of the action that created the issue, or `GITHUB_TOKEN`.
- **No loops**: a change that comes from Linear isn't sent to Linear, nor one from GitHub to GitHub (except that of a merged pull request, which isn't one of its issues), and a change to a state it is already in (resolved and resolved in the next release both count as closed) is neither recorded nor sent. The webhook with which Linear or GitHub reports one of our own changes (within 10 minutes) isn't applied either, even if it arrives out of order or there is already a newer change here. New error and regression alerts stay the same.

To connect them (the URLs are also in Settings → Integrations, with what the server is missing and the last delivery):

1. **Linear**: Settings → API → Webhooks → New webhook, with the URL `<PUBLIC_URL>/webhooks/linear` and the Issues events. Linear gives a signing secret: it goes in `LINEAR_WEBHOOK_SECRET`. Each delivery is checked with `Linear-Signature` (the hex HMAC-SHA256 of the body with that secret) and its `webhookTimestamp`: one older than a minute is rejected, and a repeated one (`Linear-Delivery`) isn't applied twice.
2. **GitHub**: in the repository (or the organization), Settings → Webhooks → Add webhook, with the URL `<PUBLIC_URL>/webhooks/github`, content type `application/json`, a secret of your choice (in `GITHUB_WEBHOOK_SECRET`) and the Issues and Pull requests events. Each delivery is checked with `X-Hub-Signature-256`, and a repeated one (`X-GitHub-Delivery`) isn't applied twice. For actions to create, close and reopen issues, `GITHUB_TOKEN` (or the action's token) needs write access to the repository's issues.

Neither route asks for a token: the signature protects them. Without their secret, they answer 503.

## Daily or weekly digest

Each project can send, every day or every week, how it went without opening the UI: it is set in Settings → Digest or with `PUT /api/0/projects/{id}/digest`. You choose the hour and the timezone (hours are those of that zone's clock, also across clock changes), the weekday for the weekly one, the language (`en` or `es`; projects that don't choose take `DEFAULT_LANGUAGE`) and one or more destinations: email (`SMTP_URL`), Slack or the signed webhook, with their details stored and masked like those of rules. The period is the one that ends at that hour: the previous 24 hours (or 7 days).

It brings, in sections, leaving out the empty ones:

- **By the numbers**: the period's events and their change from the previous one, the errors with events, the affected users and how many were marked resolved.
- **New errors** and **Regressions**, those with the most events first, up to 10, with their link.
- **Growing fastest**: those that already existed and have at least 5 more events and twice as many as in the previous period.
- **Cron problems**: monitors with failed, missed or timed-out runs.
- **Downtime**: uptime monitors with incidents in the period, how many and how long they were down within it, the longest first.
- **Release health**: the latest release, if it has sessions: crash-free sessions and users, with the title linked to the release (in the webhook, the section's `url`).

Events and users are those of kept events, so sampling and the per-issue cap lower them. A period with nothing sends a short digest that says so or, with `send_empty: false`, none. Email goes as simple HTML with a text version; Slack, as Block Kit; the webhook, with the sections as JSON and the digest in `markdown`, signed like the rules' webhook, with `type: digest` and `X-Mothlog-Event: digest`:

```json
{
  "type": "digest",
  "delivery": "outbox-913",
  "sent_at": "2026-11-23T08:00:04Z",
  "project": {"id": 4, "name": "shop"},
  "frequency": "daily",
  "language": "en",
  "period": {"start": "2026-11-22T08:00:00Z", "end": "2026-11-23T08:00:00Z", "timezone": "Europe/Madrid"},
  "title": "Daily digest for shop",
  "empty": false,
  "sections": [{"id": "new", "title": "New errors", "total": 1,
                "items": [{"title": "TypeError: …", "detail": "3 events · checkout/cart.ts", "url": "https://errors.example.com/issues/42"}]}],
  "markdown": "# Daily digest for shop\n…"
}
```

A process inside the service checks every minute whether one is due. The queued period is recorded in the database, with the project's row locked, in the same transaction that leaves one delivery per destination in the alerts' outbox, with the same retries: several replicas don't duplicate it and a restart doesn't repeat it. If the service was stopped at that hour, the last closed period's digest goes out when it starts (not the earlier ones that were missed). When it is turned on or its hour changes, the first one goes out at the next hour. "Send one now" (`POST /api/0/projects/{id}/digest/send`) sends right away the last 24 hours (or 7 days) to each destination, without going through the outbox, and says how it went for each. In the MCP, `get_digest` gives the digest of any period in Markdown.
