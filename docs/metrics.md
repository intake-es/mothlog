# Metrics

With `METRICS_TOKEN`, `GET /metrics` serves the service's metrics in the Prometheus text format to whoever sends `Authorization: Bearer <METRICS_TOKEN>`; without it, 401. Without the variable, the route answers 404 and the service counts nothing. A random token works: `openssl rand -hex 32`. Metrics carry no event data, but they do carry project names and API routes, so better not publish them without a token or outside the network of whoever reads them.

Counting happens in memory, without database queries per event. What comes from the database (alerts, monitors, projects) is read when `/metrics` is requested, in a single round trip with a 2-second limit, and kept for 10 seconds. Each replica counts its own, from when it started; for the total, `sum` in Prometheus.

| Metric | Type | Labels | What it is |
|---|---|---|---|
| `mothlog_events_received_total` | counter | `project` | events of envelopes with a valid key, before filtering |
| `mothlog_events_saved_total` | counter | `project` | events saved |
| `mothlog_events_dropped_total` | counter | `project`, `reason` | events dropped: by filters (`message`, `exception_type`, `browser_extension`, `web_crawler`, `localhost`, `environment`, `release`), by the per-issue hourly cap (`issue_limit`), by sampling (`sampled`), by the per-minute limit (`rate_limit`) or as duplicates of an `event_id` already saved (`duplicate`). Dropped sessions and stripped fields aren't here, because they aren't events: they are in `GET /api/0/projects/{id}/dropped` |
| `mothlog_envelopes_rejected_total` | counter | `code` | envelopes rejected by ingestion: 400, 401, 404, 413, 429… |
| `mothlog_ingest_duration_seconds` | histogram | | how long each accepted envelope takes, from 5 ms to 10 s |
| `mothlog_checkins_received_total` | counter | `status` | cron check-ins received: `in_progress`, `ok`, `error` or `ignored` |
| `mothlog_http_requests_total` | counter | `method`, `route`, `code` | requests to the API (`/api/0/`) and the MCP (`/mcp`), with the route as declared (`/api/0/issues/{id}/{$}`), without ids; `unmatched` if none matches |
| `mothlog_alert_outbox` | gauge | `channel`, `status` | alerts in the outbox: `pending`, `sent` or `failed` |
| `mothlog_alert_outbox_oldest_pending_seconds` | gauge | `channel` | age of the oldest pending alert; 0 if there is none |
| `mothlog_digests_sent_total` | counter | `channel` | digests sent |
| `mothlog_cron_monitors` | gauge | `status` | cron monitors: `pending`, `ok`, `error`, `missed`, `timeout` |
| `mothlog_uptime_monitors` | gauge | `status` | uptime monitors: `pending`, `up`, `down`, `paused` |
| `mothlog_project_info` | gauge | `project`, `name` | 1 per project, to put a name to the `project` label (the id) |
| `mothlog_metrics_db_up` | gauge | | 1 if the last database read for `/metrics` went well; with 0, the database metrics are missing |
| `mothlog_metrics_db_duration_seconds` | gauge | | how long that read took |
| `mothlog_db_pool_connections` | gauge | `state` | pool connections: `in_use`, `idle`, `constructing` |
| `mothlog_db_pool_max_connections` | gauge | | the pool's maximum |
| `mothlog_db_pool_waits_total`, `mothlog_db_pool_wait_seconds_total` | counter | | times and time spent waiting for a free connection |
| `go_goroutines`, `go_memstats_heap_alloc_bytes`, `go_memstats_sys_bytes`, `go_gc_cycles_total`, `process_resident_memory_bytes` | | | goroutines and the process's memory |

An envelope over the per-minute limit counts in both places: its events in `mothlog_events_dropped_total{reason="rate_limit"}` and the envelope in `mothlog_envelopes_rejected_total{code="429"}`. Events received are the sum of those saved and those dropped.

For Prometheus, with the token in a file:

```yaml
scrape_configs:
  - job_name: mothlog
    scheme: https
    metrics_path: /metrics
    authorization:
      type: Bearer
      credentials_file: /etc/prometheus/mothlog-token
    static_configs:
      - targets: ["errors.example.com"]

rule_files: [mothlog-alerts.yml]
```

And some alerts to start with, in `mothlog-alerts.yml`; the thresholds depend on your traffic:

```yaml
groups:
  - name: mothlog
    rules:
      - alert: AlertsStuck
        expr: max by (channel) (mothlog_alert_outbox_oldest_pending_seconds) > 1800
        for: 10m
        annotations:
          summary: "{{ $labels.channel }} alerts have been pending for more than half an hour"
      - alert: AlertsFailed
        expr: increase(mothlog_alert_outbox{status="failed"}[1h]) > 0
        annotations:
          summary: "{{ $labels.channel }} alerts were abandoned"
      - alert: ManyDropped
        expr: |
          sum by (project) (rate(mothlog_events_dropped_total{reason!~"duplicate"}[15m]))
            / sum by (project) (rate(mothlog_events_received_total[15m])) > 0.5
        for: 15m
        annotations:
          summary: "Project {{ $labels.project }} drops more than half of its events"
      - alert: SlowIngestion
        expr: |
          histogram_quantile(0.95, sum by (le) (rate(mothlog_ingest_duration_seconds_bucket[5m]))) > 1
        for: 10m
        annotations:
          summary: "95 % of envelopes take more than 1 s to be saved"
      - alert: ManyRejected
        expr: sum by (code) (rate(mothlog_envelopes_rejected_total{code=~"401|413|429"}[15m])) > 1
        for: 15m
      - alert: NoDatabaseRead
        expr: mothlog_metrics_db_up == 0
        for: 5m
      - alert: PoolExhausted
        expr: rate(mothlog_db_pool_waits_total[5m]) > 1
        for: 10m
```
