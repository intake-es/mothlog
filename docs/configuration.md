# Configuration

Mothlog is configured with environment variables. The same variables apply to the service and to the [command line](cli.md), which uses the same `DATABASE_URL`.

## Environment variables

| Variable | What for | Default |
|---|---|---|
| `DATABASE_URL` | `postgres://…`, or `sqlite:///path/mothlog.db` for [SQLite](sqlite.md) | required |
| `LISTEN_ADDR` | HTTP address | `:8080` (the Docker image sets `:8000`) |
| `PUBLIC_URL` | the URL people and SDKs reach the service at: it goes in the DSNs and in alert links, and is the issuer of the [MCP's OAuth](agents.md#oauth) | required for `projects create` and `projects list`; `/api/0/projects/` uses each request's host without it, and alerts go without a link |
| `LOG_LEVEL` | `debug`, `info`, `warn` or `error` | `info` |
| `DEFAULT_LANGUAGE` | `en` or `es`: the language of alerts, the Markdown for agents, the MCP, `/llms.txt` and the digest of projects that don't choose one ([Language](#language)) | `en` |
| `RATE_LIMIT_PER_MINUTE` | events per minute and project, for projects without their own limit | `60` |
| `RETENTION_DAYS` | days events, sessions, check-ins and source maps are kept | `90` |
| `SOURCEMAP_UPLOAD_TOKEN` | a token for uploading source maps, besides API tokens; without it only API tokens work | empty |
| `SLACK_WEBHOOK_URL` | a Slack incoming webhook for the [default alerts](alerts.md#default-alerts) | empty: no Slack |
| `LINEAR_API_KEY` | a Linear API key; without it no Linear issues are created | empty |
| `LINEAR_TEAM_ID` | the Linear team where issues are created | required with `LINEAR_API_KEY` |
| `LINEAR_LABEL_IDS` | comma-separated Linear label ids for every issue | empty |
| `LINEAR_API_URL` | Linear's GraphQL endpoint; only to test against a fake one | `https://api.linear.app/graphql` |
| `LINEAR_WEBHOOK_SECRET` | the secret of the [Linear webhook](alerts.md#linear-and-github-both-ways); without it `/webhooks/linear` rejects everything | empty |
| `GITHUB_TOKEN` | a GitHub token for GitHub alert actions that don't bring their own, and to close and reopen their issues | empty |
| `GITHUB_WEBHOOK_SECRET` | the secret of the GitHub webhook; without it `/webhooks/github` rejects everything | empty |
| `GITHUB_API_URL` | the GitHub API; only to test against a fake one | `https://api.github.com` |
| `TELEGRAM_API_URL` | the Telegram Bot API; only to test against a fake one | `https://api.telegram.org` |
| `SMTP_URL` | the mail server for email alerts and digests: `smtp://user:password@host:587` (STARTTLS if the server offers it) or `smtps://…:465` | empty: no email |
| `SMTP_FROM` | the sender of those emails, like `Mothlog <alerts@example.com>` | required with `SMTP_URL` |
| `GITHUB_CLIENT_ID`, `GITHUB_CLIENT_SECRET`, `GITHUB_ORG` | the GitHub OAuth App for [signing in with GitHub](#github), and the org whose active members can sign in; all three or none | empty: no GitHub login |
| `SESSION_SECRET` | signs the session cookie; 32 characters or more, required with GitHub or OIDC | empty: a random one per start with the development login |
| `OIDC_ISSUER`, `OIDC_CLIENT_ID`, `OIDC_CLIENT_SECRET` | the [OIDC provider](#oidc) for signing in; the secret can be left out for a public client | empty: no OIDC |
| `OIDC_ALLOWED_DOMAINS` | comma-separated domains of the verified email that can sign in | empty |
| `OIDC_ALLOWED_GROUPS`, `OIDC_GROUPS_CLAIM` | comma-separated groups that can sign in, and the claim they come in | empty, `groups` |
| `OIDC_ALLOW_ANY` | `1` lets anyone with an account at the issuer sign in; without it or a limit, the service doesn't start with OIDC | empty |
| `OIDC_SCOPES`, `OIDC_NAME` | the scopes requested and the name on the button ("Sign in with …") | `openid email profile`, `SSO` |
| `UI_DEV_LOGIN` | `1` lets anyone who has the key in the log sign in as `dev`, without an account; only with `LISTEN_ADDR` on loopback ([Development login](#development-login)) | empty |
| `UPTIME_ALLOW_PRIVATE` | `1` lets [uptime monitors](monitors.md#uptime-monitors) reach private networks and loopback (10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, 127.0.0.0/8, 100.64.0.0/10, fc00::/7, ::1); metadata and link-local addresses never | empty: public addresses only |
| `ALERTS_ALLOW_PRIVATE` | `1` lets alert rule actions (Slack, Discord, ntfy and the webhook) and digest destinations reach private networks and loopback, like `UPTIME_ALLOW_PRIVATE` ([Where alerts can go](alerts.md#where-alerts-can-go)) | empty: public addresses only |
| `INGEST_BATCH_MAX`, `INGEST_BATCH_WAIT` | at most this many events per write batch, and the longest a batch waits for envelopes already being read ([Batches](ingestion.md#batches-and-performance)) | `100`, `2ms` |
| `METRICS_TOKEN` | the token for `GET /metrics` ([Metrics](metrics.md)); without it the route doesn't exist and nothing is counted | empty |
| `PPROF_ADDR` | an address to serve `net/http/pprof` on, apart from the public port; only for profiling, and only on loopback (`127.0.0.1`, `[::1]` or `localhost`): with any other, the binary doesn't start | empty: no pprof |
| `GOMEMLIMIT` | Go's soft memory limit; without it, 90 % of the container's memory limit if it has one ([Memory](deploy.md#memory)) | |
| `SENTRY_IMPORT_TOKEN` | the Sentry token for [`mothlog import sentry`](import.md) | |

`GET /healthz` answers `ok` when the database responds, and 503 when it doesn't.

## Language

The UI is in English and Spanish. It starts in Spanish when the browser's language starts with `es` and in English otherwise, and can be switched from the sidebar, next to the theme, without reloading; the choice is kept in the browser.

`DEFAULT_LANGUAGE` (`en` or `es`) is the language of what the server writes for people and agents: alerts on every channel, the `context.md` and `stacktrace.md` Markdown, the MCP's descriptions and instructions, `/llms.txt`, and the digest of projects that don't pick a language of their own. Any other value stops the service from starting.

## Signing in to the UI

The UI needs someone signed in. There are three ways, and GitHub and OIDC can be configured together: the sign-in screen then shows both buttons. Without any of them, the UI says the login isn't configured, and only API tokens work.

With GitHub or OIDC, `PUBLIC_URL` has to be https (http only on `localhost`, `127.0.0.1` or `[::1]`) and `SESSION_SECRET` is required, for example from `openssl rand -base64 48`.

### GitHub

Create a GitHub OAuth App with the callback `<PUBLIC_URL>/auth/github/callback`, and set `GITHUB_CLIENT_ID`, `GITHUB_CLIENT_SECRET` and `GITHUB_ORG`. `/auth/github/login` asks for the `read:org` scope with a `state` in a signed cookie, and the callback only lets someone in when `GET /user/memberships/orgs/<GITHUB_ORG>` says `active`. The GitHub token isn't kept.

People are recognized by their GitHub account's numeric id, not by their login, which can change: someone who renames their account stays who they were, and another account that takes the login they left signs in as `login~2`, without their comments or assignments.

### OIDC

Register a web application at the provider with the redirect URI `<PUBLIC_URL>/auth/oidc/callback`, and configure it like this:

```sh
OIDC_ISSUER=https://idp.example.com        # its /.well-known/openid-configuration says the rest
OIDC_CLIENT_ID=mothlog
OIDC_CLIENT_SECRET=...
OIDC_NAME="Team SSO"                       # the button says "Sign in with Team SSO"
OIDC_ALLOWED_DOMAINS=example.com           # or, by group:
# OIDC_ALLOWED_GROUPS=engineering,support
# OIDC_GROUPS_CLAIM=groups                 # the claim the provider sends groups in
# OIDC_SCOPES="openid email profile groups"
```

`/auth/oidc/login` uses the authorization code flow with PKCE (`S256`), `state` and `nonce`, kept in a signed cookie for 10 minutes. The callback exchanges the code and checks the `id_token`: its signature with the issuer's keys (`jwks_uri`; RS*, PS*, ES* or EdDSA, never `none` or HS*), `iss`, `aud`, `exp` and the `nonce`. Keys are kept for an hour, and an unknown `kid` fetches them again, at most once a minute, to follow a rotation. If the `id_token` lacks the email or the groups, they are asked from `userinfo`. The issuer is read on the first sign-in, not on start: if it doesn't answer, the sign-in screen says so and GitHub keeps working.

Who gets in:

- With `OIDC_ALLOWED_DOMAINS`, only an email with `email_verified` from one of those exact domains (`example.com` doesn't let `mail.example.com` in).
- With `OIDC_ALLOWED_GROUPS`, only someone with one of those groups in `OIDC_GROUPS_CLAIM`, exactly, case included.
- With both, both have to hold.
- With neither, the service doesn't start, unless `OIDC_ALLOW_ANY=1`, which lets in anyone with an account at the issuer.

Mothlog keeps the provider, the `sub`, the login it shows (the email if it comes with `email_verified`; otherwise `oidc:` followed by the `preferred_username` or, without it, the `sub`), the name and the picture. It keeps no provider token. The next time, the person is recognized by the `sub`, even if their email changed. If the login already belongs to someone else (from GitHub or OIDC, case-insensitively), the newcomer becomes `login~2`, so assignees, the history and `assigned:me` don't mix them up. For people who are turned away, the log records only the reason, the `sub` and the email's domain.

The `oidc:` prefix means such a login can never be an email or a GitHub login: nobody can take a colleague's login by setting it as their `preferred_username` before the colleague signs in.

### Development login

```sh
UI_DEV_LOGIN=1 LISTEN_ADDR=127.0.0.1:8080 DATABASE_URL=sqlite://./mothlog.db mothlog
```

On start, the log shows once a link to sign in as `dev`, with a random key that changes on every start:

```json
{"level":"WARN","msg":"UI_DEV_LOGIN=1: sign in as dev with this link, which won't be shown again","url":"http://127.0.0.1:8080/auth/dev/login?key=…"}
```

Open it, or paste the key on the sign-in screen. Without the key, `/auth/dev/login` goes back to the sign-in screen without a session. It is meant for trying Mothlog on your own machine, which is what the Compose files do:

- It only works with `LISTEN_ADDR` on `127.0.0.1`, `[::1]` or `localhost`; with any other address (`:8080`, say), the service refuses to start.
- Without `PUBLIC_URL`, the UI can be opened at `http://127.0.0.1:8080`, `http://localhost:8080` or `http://[::1]:8080`. A request whose `Host` is none of those (with the port of `LISTEN_ADDR`) or the host of `PUBLIC_URL` gets nothing, even with a session, so a page using DNS rebinding, which arrives with its own host name, can't read anything.
- Without `SESSION_SECRET`, sessions are signed with a random secret and end when the service restarts.

The Compose files run Mothlog on `127.0.0.1:8000` inside the network of a Caddy proxy that publishes it on the host's `127.0.0.1:8000`. Another container on the same Docker network reaches the proxy, but without the key it can't sign in, and with a `Host` other than `localhost:8000` (the one in `PUBLIC_URL`) a session doesn't work either.

## Sessions

The session is a cookie signed with HMAC (`SESSION_SECRET`) with the login, the name and when it was issued. It is `HttpOnly`, `SameSite=Lax`, lasts 7 days and, with an https `PUBLIC_URL`, is `Secure` and carries the `__Host-` prefix (`__Host-ie_session`), so a subdomain can't set it.

Each person has in the `people` table the time from which their sessions are valid, and every request with a cookie checks it: if it can't be read, there is no session. `POST /auth/logout` clears the cookie and sets that time to now, which ends the session on the server and in all of that person's browsers. If the database doesn't answer, logout answers 500 instead of 204: the session hasn't ended on the server, and it has to be repeated.

The cookie works for `/api/0/` and `/mcp` like a token, and any request that changes something has to carry an `Origin` equal to `PUBLIC_URL`'s, or it gets a 403. Responses from `/api/0/` and `/mcp` are `Cache-Control: no-store`.

If a person can't be saved, nobody gets in, with GitHub, OIDC or the development login: without their row, their session couldn't be ended.

### Removing someone

Membership in the GitHub org, or in the OIDC provider's group or domain, is only checked when signing in. To remove someone:

1. Remove them from the GitHub org, or from the provider's group (or domain), so they can't sign in again.
2. End their open sessions and revoke the apps they connected to the MCP with OAuth:

   ```sh
   mothlog people list            # who has signed in: login, name, provider, id, last sign-in and revocation
   mothlog people revoke <login>  # the login from people list
   ```

   Without this step, their cookie would keep working for up to 7 days.

`people revoke` doesn't touch API tokens, which belong to no one: revoke the ones they used with `mothlog tokens revoke <name>`.

### People saved by older versions

Older versions saved GitHub people by their login. They move to their GitHub id the first time an account with that login signs in, and `people list` shows them as `pending` until then. If someone renamed their GitHub account before the upgrade and another account took the login they left, the first one to sign in with it keeps the old person, with their comments and assignments: check who is still `pending` and ask them to sign in soon. Logins that only differed in case were made unique: the one who signed in later became `login~N`.
