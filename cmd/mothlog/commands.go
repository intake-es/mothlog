package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"text/tabwriter"
	"time"

	"github.com/intake-es/mothlog/internal/store"
)

const usage = `usage:
  mothlog                                runs the service
  mothlog projects create <name>         creates a project and prints its DSN
  mothlog projects list                  lists the projects and their DSNs
  mothlog projects labels <name>         shows the project's own Linear labels
  mothlog projects labels <name> <id>[,<id>…]  replaces them
  mothlog projects labels <name> --clear  removes them
  mothlog projects filters <name>        shows its filters, the per-issue limit and the sampling
  mothlog projects filters <name> set <key> [<value>…]  changes one
  mothlog projects dropped <name> [<days>]  counts what was dropped, by reason (30 days by default)
  mothlog releases new <project> <version> [--env <environment>]  creates the release and, with --env, records a deploy
  mothlog projects rules <name> [<command> …]  lists, creates, edits, deletes and tests its alert rules
  mothlog tokens create <name>           creates an API token and prints it only once
  mothlog tokens list                    lists the tokens
  mothlog tokens revoke <name>           revokes the active token with that name
  mothlog people list                    lists who has signed in to the UI
  mothlog people revoke <login>          ends all of that person's sessions and revokes their apps connected to the MCP (to offboard them)
  mothlog import sentry --url <server> --org <slug> [--project <slug>…] [--since <date>] [--events <N>] [--dry-run]
                                         imports from Sentry, with the token in SENTRY_IMPORT_TOKEN
  mothlog version                        prints the binary's version, commit and date
  mothlog help                           prints this help`

func runCommand(args []string, out io.Writer) error {
	if args[0] != "tokens" && args[0] != "releases" && args[0] != "people" {
		return fmt.Errorf("unknown command %q\n%s", args[0], usage)
	}
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: max(cfg.LogLevel, slog.LevelWarn)}))
	db, err := store.OpenForCommand(ctx, cfg.DatabaseURL, log)
	if err != nil {
		return err
	}
	defer db.Close()
	switch args[0] {
	case "releases":
		return releasesCommand(ctx, db, args[1:], out)
	case "people":
		return peopleCommand(ctx, db, args[1:], out, time.Now)
	}
	return tokensCommand(ctx, db, args[1:], out)
}

func tokensCommand(ctx context.Context, db *store.Store, args []string, out io.Writer) error {
	switch {
	case len(args) == 2 && args[0] == "create":
		_, secret, err := db.CreateToken(ctx, args[1])
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(out, "%s\n\nSave it now: it won't be shown again.\n", secret)
		return err
	case len(args) == 1 && args[0] == "list":
		tokens, err := db.ListTokens(ctx)
		if err != nil {
			return err
		}
		tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "NAME\tCREATED\tLAST USED\tREVOKED")
		for _, t := range tokens {
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", t.Name, day(&t.CreatedAt), day(t.LastUsedAt), day(t.RevokedAt))
		}
		return tw.Flush()
	case len(args) == 2 && args[0] == "revoke":
		err := db.RevokeToken(ctx, args[1])
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("there is no active token named %q", args[1])
		}
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(out, "token %q revoked\n", args[1])
		return err
	}
	return errors.New(usage)
}

func day(t *time.Time) string {
	if t == nil {
		return "-"
	}
	return t.UTC().Format("2006-01-02 15:04 UTC")
}
