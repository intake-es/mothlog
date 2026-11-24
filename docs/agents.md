# Connecting an agent

Mothlog has an MCP server at `/mcp` with which an agent can take an error from start to finish: find it, read its whole context, fix it, leave a record of what it did and mark its status. Everything it does goes in the error's history under its name, and shows in the UI.

`GET /llms.txt`, public and without a token, describes the service, the API and the MCP for a model.

The server speaks Streamable HTTP without sessions: each request is a `POST` to `<PUBLIC_URL>/mcp`. Any replica serves any request. It takes two kinds of credentials:

- **An API token** (`ml_…`), sent as `Authorization: Bearer <token>`. It works with every client and every tool, and is the way for an agent without a browser (CI, a server). What the agent does goes under the token's name.
- **OAuth**, for clients that support MCP authorization and send it back to an https or loopback address, such as Claude Code: you give the client only the URL, and sign in with your UI login in the browser. What it does goes under your name, "via" the client.

## 1. Create a token

One token per agent, with a name that says who it is: it is the author of its comments and status changes.

```sh
mothlog tokens create backend-agent   # prints the token (ml_…) only once
mothlog tokens list
mothlog tokens revoke backend-agent   # if it leaks or is no longer needed
```

With Docker Compose, prefix the commands with `docker compose exec mothlog /mothlog` instead of `mothlog` ([Command line](cli.md)).

Only the token's SHA-256 is stored. Tokens don't have different permissions: with one, the agent reads every error of every project, with what their events carry (users, requests, breadcrumbs), comments and changes states through the MCP, and through the API can also edit filters and alert rules and record releases. Keep it in the client's secret store, not in a file in the repository, and revoke it when the agent stops using it.

## 2. Connect the client

The examples use `https://errors.example.com`; with the [quick start](../README.md#quick-start) it is `http://localhost:8000`.

### Claude Code

With a token:

```sh
claude mcp add --transport http mothlog https://errors.example.com/mcp \
  --header "Authorization: Bearer $MOTHLOG_TOKEN"
```

With `--scope project` it goes in the repository's `.mcp.json` for the whole team; in that case, leave the header with the variable (`"Authorization": "Bearer ${MOTHLOG_TOKEN}"`) and let each person set theirs in their environment.

With OAuth, without a token (the server needs `PUBLIC_URL` and a [login](configuration.md#signing-in-to-the-ui)):

```sh
claude mcp add --transport http mothlog https://errors.example.com/mcp
```

Then, in Claude Code, `/mcp` → `mothlog` → Authenticate: it opens the consent screen in the browser.

### Other clients

A client connects with OAuth only if every redirect URI it registers is https or http to `127.0.0.1`, `[::1]` or `localhost`. Some clients register a custom-scheme URI as well (Cursor sends a `cursor://…` one), and then the whole registration is rejected: use a token with those.

Clients that read their configuration as JSON (the usual `mcpServers` shape) accept something like this:

```json
{
  "mcpServers": {
    "mothlog": {
      "type": "http",
      "url": "https://errors.example.com/mcp",
      "headers": { "Authorization": "Bearer ${MOTHLOG_TOKEN}" }
    }
  }
}
```

From code, with the official MCP SDK for Go (`github.com/modelcontextprotocol/go-sdk/mcp`):

```go
type bearer struct{ token string }

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

client := mcp.NewClient(&mcp.Implementation{Name: "my-agent", Version: "1.0"}, nil)
cs, err := client.Connect(ctx, &mcp.StreamableClientTransport{
	Endpoint:   "https://errors.example.com/mcp",
	HTTPClient: &http.Client{Transport: bearer{os.Getenv("MOTHLOG_TOKEN")}},
}, nil)
// cs.CallTool(ctx, &mcp.CallToolParams{Name: "list_issues", Arguments: map[string]any{"query": "is:open"}})
```

To try it by hand without a client, the same protocol with `curl`:

```sh
curl -s https://errors.example.com/mcp \
  -H "Authorization: Bearer $MOTHLOG_TOKEN" \
  -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}'
```

## 3. The tools

| Tool | What for | Read-only |
|---|---|---|
| `list_issues` | search errors with the [search language](api.md#search): `is:open environment:production`, `is:regressed release:1.4.0`, `TypeError browser:Safari` | yes |
| `get_issue_context` | the `context.md`: everything needed to fix an error, in one Markdown | yes |
| `get_issue`, `get_issue_tags`, `get_stacktrace` | the issue as JSON, its tags and the `stacktrace.md` | yes |
| `list_events`, `get_event` | an issue's events and a whole event | yes |
| `list_comments` | the history: comments, status changes and assignments | yes |
| `list_assignees` | who can be assigned: people who have signed in to the UI and active tokens | yes |
| `list_releases`, `get_release` | releases and what one brought (new, regressions, those that stopped) and its health (crash-free sessions and users, adoption) | yes |
| `list_monitors`, `get_monitor` | crons with their state and the next expected run, and one's history; uptime monitors (`uptime` in `list_monitors`, `type` `uptime` in `get_monitor`) with their state, percentage and incidents | yes |
| `get_digest` | a project's [digest](alerts.md#daily-or-weekly-digest) for any period, in Markdown | yes |
| `assign_issue` | assign an error: `"assignee": "me"` for oneself, a login, another token's name, or empty to unassign; idempotent | no |
| `add_comment` | comment in Markdown | no |
| `set_issue_status` | `resolved_next`, `resolved`, `silenced` or `open`, with an optional `comment` attached to the change; idempotent | no |

The read tools take `environment`, like the API. The server sends the workflow below in its instructions, so a client that reads them needs nothing else. Tool descriptions and instructions are in the language of `DEFAULT_LANGUAGE`.

## 4. The workflow

1. **Find the error**: `list_issues` with `{"query": "is:open is:unassigned project:backend \"division by zero\""}`.
2. **Assign it to yourself**: `assign_issue` with `{"issue_id": 42, "assignee": "me"}`. It is assigned to the token (or to you, with OAuth), the team sees it in the list and on the error's page, and the agent finds its own with `list_issues` and `{"query": "assigned:me"}`.
3. **Read its context**: `get_issue_context` with `{"issue_id": 42}`. It brings the summary, releases, environments, the most frequent tags, the stack trace with the code of the app's own frames, breadcrumbs, the request, the user and the history, and ends with what the agent can do. Over 64 KB, it drops breadcrumbs, library frames and old history, and says so at the top.
4. **Fix it** in the code.
5. **Leave a record**: `add_comment` with `{"issue_id": 42, "body": "Cause: average() divides by shipments, which is 0 for orders not shipped yet."}`.
6. **Mark the status**: `set_issue_status` with `{"issue_id": 42, "status": "resolved_next", "comment": "Fixed in abc123: returns 0 without shipments."}`. In `resolved_next`, the error becomes resolved only when a release later than its last event's is deployed, and if it comes back in it, it reopens as a regression; use `resolved` if the fix is already deployed.
7. **Check it**: `list_comments` with `{"issue_id": 42}` shows the assignment, the comment, the status change with its comment and, after the deploy, the move to resolved.

Without MCP, the same with the API: `POST /api/0/issues/42/assignee` with `{"assignee": "me"}`, `GET /api/0/issues/42/context.md`, `POST /api/0/issues/42/comments/` and `POST /api/0/issues/42/status`.

In the UI, "Copy for an agent" on an error's page copies the same `context.md`, to paste it to an agent that doesn't have the MCP.

## 5. Assigning an agent

An agent is its token: it is assigned by the token's name. There are three ways:

- The agent itself, through the MCP: `assign_issue` with `{"issue_id": 42, "assignee": "me"}`.
- A person, on the error's page: the "Assignee" picker shows the people who have signed in to the UI and, under "Agents", the active tokens.
- Through the API, with the token's name: `POST /api/0/issues/42/assignee` with `{"assignee": "backend-agent"}` (and `"kind": "token"` if a person has that login).

Each assignment goes in the history with who made it. If the token is revoked, the error stays assigned to its name and shows as inactive; to hand it to another, assign it again.

## OAuth

With `PUBLIC_URL` and a login in the UI (GitHub, OIDC or `UI_DEV_LOGIN`), the MCP also accepts OAuth 2.1 as the MCP specification asks for it. Without `PUBLIC_URL` it isn't offered and only tokens work.

1. **Discovery.** Without credentials, `/mcp` answers 401 with `WWW-Authenticate: Bearer resource_metadata="<PUBLIC_URL>/.well-known/oauth-protected-resource/mcp"`. That metadata (RFC 9728, also at `/.well-known/oauth-protected-resource`) says the resource is `<PUBLIC_URL>/mcp` and its authorization server is the service itself, with its metadata at `/.well-known/oauth-authorization-server` (RFC 8414).
2. **Registration.** `POST /oauth/register` (RFC 7591) registers a public client, without a secret, with 1 to 10 redirect URIs that must all be https, or http to loopback (`127.0.0.1`, `[::1]`, `localhost`); one with any other scheme rejects the registration with `invalid_redirect_uri`. An https URL also works as `client_id` (Client ID Metadata Document): it is fetched with 5 s and 16 KB at most, without following redirects, and never from private, metadata or link-local addresses. Registrations that are never used are deleted after 24 hours. Each address can register 30 clients and ask for 120 authorizations every 10 minutes (then, 429 with `Retry-After`).
3. **Consent.** `GET /oauth/authorize` requires a `redirect_uri` (one of those registered; on loopback the port may change), PKCE `S256` and `resource=<PUBLIC_URL>/mcp` (RFC 8707), and sends the person to the UI's consent screen, which asks them to sign in if they have no session. It shows the client's name (with its initial, without fetching its logo from anywhere), where it goes back to, and a checkbox per scope: `read` (the read tools, checked) and `write` (`add_comment`, `assign_issue` and `set_issue_status`, unchecked). What is checked is granted, and never more than was asked for.
4. **Tokens.** `POST /oauth/token` exchanges the code (valid for 2 minutes and once: reusing it revokes what came from it) for a one-hour access token (`mlo_…`) and a 30-day refresh token (`mlr_…`) that changes on every use; reusing a spent refresh token revokes the whole grant. Only their SHA-256 is stored. They only work for `/mcp`, not for the API. `POST /oauth/revoke` (RFC 7009) revokes a token: a refresh token, with its whole grant.

With an OAuth token, `tools/list` only shows the tools its scopes allow, and calling another one returns an error that says which scope is missing. What it does goes in the history as the person who granted it, with the client (`"author": {"name": "ana", "kind": "user", "via": "Claude Code"}`, "ana via Claude Code" in the UI), and `assigned:me` and `"assignee": "me"` are that person.

Each person sees their grants in **Connected apps** (their account menu in the UI): the client, its scopes, and when and from which address they were last used (behind a proxy on loopback or on the private network, the last address of `X-Forwarded-For`), and can revoke them. `mothlog people revoke <login>` revokes all of theirs too; signing out of the UI doesn't touch them.
