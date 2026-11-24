# Command line

The `mothlog` binary is both the service and its command line. Without arguments it runs the service; with a command, it runs it against the same `DATABASE_URL` and exits. Commands can run while the service is running, with Postgres and with SQLite. `mothlog help` lists them all.

In the Docker image the binary is `/mothlog`. With Compose, run commands in the running container:

```sh
docker compose exec mothlog /mothlog projects list
```

In production, run them as a one-off task with the service's configuration ([Deploy](deploy.md#the-command-line-in-production)).

## Projects

```sh
mothlog projects create backend                 # prints the DSN: https://<key>@<host of PUBLIC_URL>/<id>
mothlog projects list                           # id, name and DSN
mothlog projects labels backend                 # shows its own Linear labels
mothlog projects labels backend <id>,<id>       # replaces them
mothlog projects labels backend --clear         # removes them
mothlog projects filters backend                # shows its filters, the per-issue cap and the sampling
mothlog projects filters backend set <key> [<value>…]   # changes one
mothlog projects dropped backend [<days>]       # what was dropped, by reason; 30 days by default
mothlog projects rules backend [<command> …]    # its alert rules
```

`projects create` and `projects list` need `PUBLIC_URL` to write the DSN. The UI doesn't create projects: it says so when there are none, and shows them once they exist.

Linear issues of a project carry two sets of labels: the ones in `LINEAR_LABEL_IDS`, for every project, and the project's own, set with `projects labels`, for whatever each team wants (who owns it, a priority, something another service picks up). The ids are Linear's, not the label names.

The keys of `projects filters set` are in [Filters, cap and sampling](ingestion.md#filters-cap-and-sampling); with no values, a list is emptied. The commands of `projects rules` are in [Alert rules](alerts.md#alert-rules).

## Releases

```sh
mothlog releases new web web@1.4.0                     # creates it
mothlog releases new web web@1.4.0 --env production    # and records a deploy to production
```

More in [Releases, deploys and environments](sdks.md#releases-deploys-and-environments).

## API tokens

API tokens open the [REST API](api.md) and the [MCP server](agents.md):

```sh
mothlog tokens create claude   # prints the token (ml_…) only once; only its SHA-256 is stored
mothlog tokens list            # name, created, last used and revoked
mothlog tokens revoke claude   # revokes the active token with that name
```

A token's name is who it is: it is the author of its comments and status changes, and how an agent is assigned an issue. Tokens created before the rename to Mothlog start with `ie_` and still work.

`tokens create` prints the token once, and it stays wherever the output of that command stays: if your platform keeps the logs of one-off tasks, whoever reads those logs can read it. If more people can read them than should use the API, revoke it and create another one somewhere else.

## People

```sh
mothlog people list             # who has signed in to the UI: login, name, provider, id, last sign-in and revocation
mothlog people revoke <login>   # ends all their sessions and revokes the apps they connected to the MCP
```

`people revoke` is the step to remove someone ([Removing someone](configuration.md#removing-someone)).

## Import

```sh
mothlog import sentry --url https://sentry.io --org my-org --dry-run
```

See [Import from Sentry](import.md).

## Version

```sh
mothlog version   # the version, commit and date it was built from
```
