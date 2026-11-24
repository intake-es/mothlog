package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/intake-es/mothlog/internal/store"
)

func peopleCommand(ctx context.Context, db *store.Store, args []string, out io.Writer, now func() time.Time) error {
	switch {
	case len(args) == 1 && args[0] == "list":
		people, err := db.People(ctx)
		if err != nil {
			return err
		}
		tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "LOGIN\tNAME\tPROVIDER\tID\tLAST LOGIN\tSESSIONS REVOKED")
		for _, p := range people {
			id := p.Subject
			// Una fila de GitHub de antes de identificar por id: la reclama
			// la próxima cuenta que entre con ese login.
			if p.Provider == store.LoginGitHub && strings.HasPrefix(p.Subject, store.LegacyGitHubSubject("")) {
				id = "pending"
			}
			revoked := "-"
			if p.SessionsValidSince.Unix() > 0 {
				revoked = day(&p.SessionsValidSince)
			}
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", p.Login, p.Name, p.Provider, id, day(&p.LastLoginAt), revoked)
		}
		return tw.Flush()
	case len(args) == 2 && args[0] == "revoke":
		login, err := db.RevokeSessions(ctx, args[1], now())
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("nobody has signed in with the login %q", args[1])
		}
		if err != nil {
			return err
		}
		grants, err := db.RevokeOAuthGrantsOf(ctx, login, now())
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(out, "sessions of %q revoked: they have to sign in again\n", login)
		if err == nil && grants > 0 {
			_, err = fmt.Fprintf(out, "MCP OAuth grants revoked: %d\n", grants)
		}
		return err
	}
	return errors.New(usage)
}
