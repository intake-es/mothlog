# Deploy

Mothlog is one process that serves the ingestion endpoint, the UI, the API and the MCP on one port, and runs its background jobs (alerts, monitors, digests, retention) inside it. It needs Postgres 16, with a database and a user that owns it, or a [SQLite](sqlite.md) file on a local disk. Migrations run on start.

## The image

The published image is `ghcr.io/intake-es/mothlog` (see [Install](../README.md#install)). To build the same image from the source, the `Dockerfile` at the root builds the UI and the binary and leaves a distroless image, without a shell, that listens on port 8000:

```sh
docker build -t mothlog .
docker run -p 8000:8000 -e DATABASE_URL=postgres://… -e PUBLIC_URL=https://errors.example.com mothlog
```

## Replicas

With Postgres, several replicas can share the database: alerts, Linear and GitHub sync, monitors, digests, retention and the conversion of old events share the work with `FOR UPDATE SKIP LOCKED`, so nothing is done twice. Two things are per replica: the per-minute rate limit of each project, and the 30 seconds for which a replica remembers whether a project has alert rules. With SQLite there is a single instance.

`GET /healthz` works as a health check: `ok` when the database answers.

## Configuration

Every variable is in [Configuration](configuration.md). For a deployment with the UI:

- `DATABASE_URL`, with `sslmode=require` if the database is on another machine.
- `PUBLIC_URL`: the https URL the service is reached at.
- A login: a GitHub OAuth App with the callback `<PUBLIC_URL>/auth/github/callback` and `GITHUB_CLIENT_ID`, `GITHUB_CLIENT_SECRET` and `GITHUB_ORG`, or an [OIDC provider](configuration.md#oidc); and `SESSION_SECRET`, 32 characters or more (`openssl rand -base64 48`).
- If you want them: `SOURCEMAP_UPLOAD_TOKEN` (a random token), `SLACK_WEBHOOK_URL`, and `LINEAR_API_KEY` with `LINEAR_TEAM_ID`.

`DATABASE_URL`, `SESSION_SECRET`, `GITHUB_CLIENT_SECRET`, `OIDC_CLIENT_SECRET`, `SOURCEMAP_UPLOAD_TOKEN`, `SLACK_WEBHOOK_URL`, `LINEAR_API_KEY`, `LINEAR_WEBHOOK_SECRET`, `GITHUB_TOKEN`, `GITHUB_WEBHOOK_SECRET`, `SMTP_URL` and `METRICS_TOKEN` are secrets: better from your platform's secret manager than in plain text in the service definition.

Don't set `UI_DEV_LOGIN` in production.

## Memory

If the container has a memory limit (the `memory.max` of its cgroup, or `memory.limit_in_bytes` with cgroup v1) and `GOMEMLIMIT` isn't set, the service sets `GOMEMLIMIT` to 90 % of that limit on start and says so in the log. That way Go's garbage collector works harder before the container reaches its limit and gets killed, with the right value for each deployment and without repeating it in the image or the Compose files. With `GOMEMLIMIT` in the environment, that one wins. Without a container limit (the example Compose files set none) nothing changes; to set one, `mem_limit` in Compose or `--memory` in `docker run`.

At 0.25 vCPU, Mothlog takes over 200 events per second staying under 50 MB of RSS ([Batches and performance](ingestion.md#batches-and-performance)).

## The command line in production

The [commands](cli.md) are in the same image and use the same `DATABASE_URL`, so they run as a one-off task with the service's configuration:

```sh
docker run --rm -e DATABASE_URL=… -e PUBLIC_URL=https://errors.example.com ghcr.io/intake-es/mothlog:latest projects create api
docker run --rm -e DATABASE_URL=… -e PUBLIC_URL=https://errors.example.com ghcr.io/intake-es/mothlog:latest projects list
docker run --rm -e DATABASE_URL=… ghcr.io/intake-es/mothlog:latest tokens create claude
```

`projects create` prints the DSN (`https://<key>@errors.example.com/<id>`) for the `dsn` option of each app's SDK. `tokens create` prints the token once; mind where your platform keeps the output of one-off tasks ([API tokens](cli.md#api-tokens)).
