# Monitors

Both kinds are in the UI under Monitors, with their state, their latest runs or checks and the history of each one.

## Cron monitors

A cron that stops running, or that fails without raising an exception, sends no error. To find out, each run sends check-ins: `in_progress` when it starts and `ok` or `error` when it ends, with its duration. They are sent by the Sentry SDK (the envelope's `check_in` item) or by `sentry-cli monitors run`, to the same DSN as errors.

A monitor belongs to a project and is identified by its slug. It has its schedule (a five-field crontab with its timezone, or every N minutes, hours, days, weeks, months or years), how late a check-in may arrive (`checkin_margin`, in minutes, 1 by default), how long a run may last (`max_runtime`, 30 by default) and the environment it watches. It is created by the first check-in that brings `monitor_config`, and the `monitor_config` of the following ones updates it (what it doesn't bring goes back to its default); it can also be created, edited, muted and deleted with the [API](api.md). A check-in for a monitor that doesn't exist and without `monitor_config` is ignored. A monitor created by a check-in works for any environment; with `environment` set through the API, check-ins from other environments are ignored, their `monitor_config` included. A check-in only creates monitors while the project has fewer than 500; above that, they are created through the API. Check-ins count in the project's per-minute limit, like events (the 429 carries the `monitor` category), and a malformed one is skipped without rejecting the rest of the envelope.

With Python:

```python
import sentry_sdk
from sentry_sdk.crons import monitor

sentry_sdk.init(dsn="https://<key>@errors.example.com/1", environment="production")

@monitor(
    monitor_slug="nightly-cleanup",
    monitor_config={
        "schedule": {"type": "crontab", "value": "0 3 * * *"},
        "timezone": "Europe/Madrid",
        "checkin_margin": 5,
        "max_runtime": 20,
    },
)
def cleanup():
    ...
```

For another kind of schedule, `{"type": "interval", "value": 2, "unit": "hour"}`. The other SDKs have the same (`Sentry.withMonitor` in JavaScript, `capture_checkin` by hand in Python).

With `sentry-cli`, from the crontab itself, wrapping the command (an exit code other than 0 is `error`):

```sh
export SENTRY_DSN=https://<key>@errors.example.com/1
sentry-cli monitors run -s "0 3 * * *" --timezone Europe/Madrid --check-in-margin 5 --max-runtime 20 \
  nightly-cleanup -- /opt/cleanup.sh
```

**States.** A monitor's state is that of its last closed check-in: `ok`, `error`, `missed` (the next one didn't arrive within the margin) or `timeout` (an `in_progress` that wasn't closed before `max_runtime`); `pending` until the first one. The next expected run is counted from each check-in that arrives; until the first one, none is expected. A process inside the service goes over the missing and overdue ones every 15 seconds, without an external cron; with several replicas, each takes monitors and runs with `FOR UPDATE SKIP LOCKED`, so nothing is marked twice. If the service was stopped and several runs were missed, one `missed` is recorded and the next one that can still arrive is expected. An `ok` that arrives after a `timeout` keeps the duration, but the run stays `timeout`.

**Alerts.** They alert on a change of state: when they start failing, not arriving or running too long (and when they change from one to another), and when they work again. Without cron rules, a project alerts through the [default channels](alerts.md#default-alerts) about those that fail, don't arrive or run too long; with them, as they say ([Alert rules](alerts.md#alert-rules)). A muted monitor doesn't alert, but keeps recording its runs.

Runs are kept as long as events (`RETENTION_DAYS`), except each monitor's last one.

## Uptime monitors

To find out that a website or an API stops answering, the service requests its URL every so often. A monitor belongs to a project: name, URL, method (`GET` or `HEAD`), how often (from 30 s to 1 h), the maximum response time, the status codes that count as up (2xx by default) and how many failures in a row mean down (2 by default). They are created, edited, paused and deleted in the UI (Monitors → New monitor) or with the [API](api.md).

**Checks.** A process inside the service looks every 5 seconds for the monitors that are due and claims them with `FOR UPDATE SKIP LOCKED`, moving their next check one interval; with several replicas, each monitor is checked by only one. It makes up to 8 requests at a time, follows up to 5 redirects, doesn't read the response body, and keeps the status code, the duration and the error. A failure is a status code outside the expected ones, not answering in time or not being able to connect.

**Addresses.** URLs are set by whoever uses the UI, but the service requests them from its own network. It never requests cloud metadata or link-local addresses (169.254.0.0/16, fe80::/10, `fd00:ec2::254`, `100.100.100.200`) or reserved ones, also after a redirect or when DNS resolves there: the service resolves the name and connects to the address it checked. A URL that already points there is rejected when saved. Private networks and loopback are only requested with `UPTIME_ALLOW_PRIVATE=1`; without it, the check fails with the reason. The proxy from the environment variables isn't used.

**Incidents.** On reaching the failures in a row, an incident opens, starting at the first of them, with its error, and the first good check closes it with its duration. Uptime over the last 24 h and 30 days is the time outside incidents since the monitor exists. A paused monitor isn't checked and doesn't alert; if it was down, its incident closes when it is paused, without an alert, and when resumed it starts again as pending.

**Alerts.** When it goes down and when it comes back, with how long the incident lasted. Without uptime rules, through the default channels; with them, as they say ([Alert rules](alerts.md#alert-rules)). Linear only creates the issue for going down.

Checks are kept for 7 days; incidents, forever.
