# Contributing

Thanks for helping. This page explains how to build Mothlog, run the tests and the checks, and what a pull request needs. To report a vulnerability, don't open an issue: see [SECURITY.md](SECURITY.md).

## Setting up

You need:

- Go, the version in `go.mod` (with `GOTOOLCHAIN=auto`, the default, any recent Go downloads it);
- Node 22 and npm, for the UI in `web/`;
- Docker, for the Postgres used in development and tests (optional: everything also runs on SQLite).

```sh
git clone https://github.com/intake-es/mothlog.git
cd mothlog
cd web && npm ci && npm run build && cd ..   # builds the UI into internal/ui/dist/app, embedded in the binary
go build -o mothlog ./cmd/mothlog
```

Without building the UI, `go build` and `go test` still work, and `/` is a page that explains how to build it.

### Running it

The quickest is SQLite with the development login:

```sh
UI_DEV_LOGIN=1 LISTEN_ADDR=127.0.0.1:8080 DATABASE_URL=sqlite://./mothlog.db go run ./cmd/mothlog
```

Open the `…/auth/dev/login?key=…` link from the log ([Development login](docs/configuration.md#development-login)). `.gitignore` covers `*.db` files and `.env`.

With Postgres, `make run` starts the development Postgres 16 (the `db` service of the `dev` profile in `docker-compose.yml`, on `127.0.0.1:5433`) and runs the service on `:8080`, without a login; add the variables you need, for example `UI_DEV_LOGIN=1 LISTEN_ADDR=127.0.0.1:8080 make run`.

To work on the UI with live reload, run the service as above but with `PUBLIC_URL=http://localhost:4200`, and in another terminal:

```sh
cd web
npm start   # ng serve on :4200, with /api and /auth proxied to 127.0.0.1:8080
```

Then open the sign-in link from the service's log, which now points to `localhost:4200`.

## Tests

```sh
go test ./...
```

Tests that use the database (`internal/store`, `internal/ingest`, `internal/api`, `internal/alert`, the commands…) run on SQLite, in a temporary file per test, unless `MOTHLOG_TEST_DATABASE_URL` is set: then they run on that Postgres, with a schema of their own per test that is dropped at the end. Golden outputs (the API, the MCP, the Markdown and the digest) are the same with both engines. The few tests that only make sense with Postgres (its migrations, converting old events, several replicas at once) are skipped with SQLite and say why.

With Postgres 16 (it has to be 16, from the official image: one test compares the text ordering with its `en_US.UTF-8` collation):

```sh
docker compose up -d --wait db
MOTHLOG_TEST_DATABASE_URL='postgres://mothlog:mothlog@127.0.0.1:5433/mothlog?sslmode=disable' go test ./...
```

or with a throwaway container on a free port:

```sh
docker run -d --rm --name mothlog-test-pg -p 127.0.0.1::5432 -e POSTGRES_PASSWORD=x postgres:16
port=$(docker port mothlog-test-pg 5432 | head -n 1 | cut -d: -f2)
MOTHLOG_TEST_DATABASE_URL="postgres://postgres:x@127.0.0.1:$port/postgres?sslmode=disable" go test ./...
docker stop mothlog-test-pg
```

The UI's tests:

```sh
cd web
npm test   # Vitest
```

`testdata/envelopes/` are envelopes recorded from real SDKs; how they were recorded and what each one must do is in [tools/fixtures/README.md](tools/fixtures/README.md). `tools/sentry-cli/prueba.sh` is an end-to-end test with the real `sentry-cli`, and `make carga` a load test ([Batches and performance](docs/ingestion.md#batches-and-performance)).

## Checks

CI runs these on every pull request, and they have to pass:

| Check | Command |
|---|---|
| Go vet | `go vet ./...` |
| Formatting | `gofmt -l .` prints nothing |
| Go tests, with SQLite and with Postgres 16 | `go test ./...` and the same with `MOTHLOG_TEST_DATABASE_URL` |
| Lint | `golangci-lint run` (v2.15.0) |
| Go vulnerabilities | `go run golang.org/x/vuln/cmd/govulncheck@v1.10.0 ./...` |
| UI dependencies | `npm audit --omit=dev --audit-level=high`, in `web/` |
| UI lint | `npm run lint` (Biome), in `web/` |
| UI tests | `npm test`, in `web/` |
| UI build | `npm run build`, in `web/` |

`make lint` runs `go vet`, `gofmt` and `golangci-lint`. The Go tests include `tools/enlaces`, which checks that every relative link in the Markdown files points to a file and a heading that exist; `go run ./tools/enlaces` runs it on its own.

## Conventions

- **Commits**: `type(scope): description`, with `feat`, `fix`, `docs`, `test`, `refactor`, `chore`…, and the scope being the area it touches (`ingest`, `mcp`, `alerts`, `ui`…). The body says what changed and why. The history, code comments and identifiers are in Spanish; English is fine if you don't write Spanish.
- **What users see is in English and Spanish.** UI texts are in `web/src/app/core/i18n/en.ts` and `es.ts`, with the same keys; texts the server writes (alerts, Markdown, the MCP) go through `lang.T` with both. The API answers in English.
- **Migrations**: a Postgres migration in `migrations/` and its SQLite version in `migrations/sqlite/`, with the same number. Never change a migration that has been released.
- **Dependencies**: Dependabot keeps Go modules, npm packages, GitHub Actions (pinned by SHA) and Docker images (pinned by digest) up to date. To add an npm dependency, use npm 11 or newer, which resolves this lockfile's peers.
- **Documentation**: if a change alters a command, a variable, a route or a behaviour that is documented, update the docs in the same pull request.

## Pull requests

- One change per pull request, on a branch from `main`.
- Tests for what changes, in the package it changes; a bug fix comes with a test that fails without it.
- All the checks above pass.
- The description says what changes and why, how you tried it beyond the tests, and anything left open. For UI changes, add screenshots.
- No secrets, tokens or personal data in the code, the fixtures or the screenshots.

A maintainer reviews it and merges it squashed, with the title as the commit message.

## Releases

Maintainers publish a release by pushing a `vX.Y.Z` tag on a commit of `main`:

```sh
git tag v1.0.0 origin/main && git push origin v1.0.0
```

`.github/workflows/release.yml` runs in the `release` environment: its first step fails if the tagged commit isn't on `main`, and then GoReleaser builds the UI and the four binaries, with the version, commit and date inside (`mothlog version`), and publishes the release on GitHub, with notes from the commits, and the image on `ghcr.io`.

Configured on GitHub, outside the code:

- **Settings → Environments → `release`**: required reviewers, so a release waits for a maintainer's approval, and deployment branches and tags limited to `v*` tags.
- **Settings → Rules**: a tag ruleset for `v*` so only maintainers can create or move release tags.
- **The `ghcr.io` package**: a new package is private; after the first release, make it public in Package settings → Change visibility, or `docker pull` and the Compose files fail with `denied` for everyone else.
- **Settings → Code security**: private vulnerability reporting on, for [SECURITY.md](SECURITY.md).

To try a release locally without publishing anything:

```sh
go run github.com/goreleaser/goreleaser/v2@v2.20.3 release --snapshot --clean
```

It leaves the `.tar.gz` files and `checksums.txt` in `dist/`, and an image per architecture in Docker, with `-amd64` and `-arm64` at the end of the tag.
