# Security policy

## Reporting a vulnerability

Please report vulnerabilities privately, through GitHub's private vulnerability reporting: on the repository, open **Security → Report a vulnerability**, or go straight to <https://github.com/intake-es/mothlog/security/advisories/new>. Only the maintainers see the report, and the fix can be prepared with you in a private fork.

Don't open a public issue, pull request or discussion about a vulnerability before it is fixed and published.

A good report says:

- which version or commit is affected, and how Mothlog is configured (Postgres or SQLite, which login, which variables matter);
- what an attacker needs (an API token, a UI session, a project's DSN key, nothing) and what they gain;
- the steps to reproduce it, ideally a request or a short script.

## Supported versions

Fixes go into the latest release only: there are no backports to older ones. Upgrade to the newest version to get security fixes. The `latest` Docker image and the newest release on GitHub always have them.

| Version | Fixes |
|---|---|
| The latest release | yes |
| Older releases | no |

## What to expect

- We acknowledge the report within 5 working days.
- Within 10 working days we tell you whether we consider it a vulnerability, how severe, and what we plan to do.
- We keep you informed until it is fixed, and agree with you on when to publish. We aim to release a fix within 90 days of the report, much sooner for serious issues.
- Once fixed, we publish a GitHub security advisory with the affected and fixed versions, and credit you if you want to be credited.

## Scope

In scope: the Mothlog service and binary, its Docker image, and the `docker-compose` files as published here. Note that the Compose files are meant for trying Mothlog on your own machine: they sign in without an account (`UI_DEV_LOGIN`) and only listen on `127.0.0.1`, by design.

Out of scope: vulnerabilities in dependencies that Mothlog doesn't reach (report those upstream), and problems that need an attacker who already controls the server, its environment variables or its database.
