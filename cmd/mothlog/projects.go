package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/intake-es/mothlog/internal/alert"
	"github.com/intake-es/mothlog/internal/store"
)

const projectsUsage = "usage: mothlog projects create <name> | projects list | projects labels <name> [<id>[,<id>…] | --clear]" +
	" | projects filters <name> [set <key> [<value>…]] | projects dropped <name> [<days>]" +
	" | projects rules <name> [show|create|edit|delete|test …]"

func projects(cfg config, args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New(projectsUsage)
	}
	if !slices.Contains([]string{"labels", "filters", "dropped", "rules"}, args[0]) {
		if _, err := dsn(cfg.PublicURL, store.Project{}); err != nil {
			return err
		}
	}
	ctx := context.Background()
	// Los logs de las migraciones van a stderr para no mezclarse con la salida.
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: cfg.LogLevel}))
	db, err := store.OpenForCommand(ctx, cfg.DatabaseURL, log)
	if err != nil {
		return err
	}
	defer db.Close()

	switch {
	case args[0] == "create" && len(args) == 2:
		name := strings.TrimSpace(args[1])
		if name == "" {
			return errors.New("the name can't be empty")
		}
		key, err := newPublicKey()
		if err != nil {
			return err
		}
		p, err := db.CreateProject(ctx, name, key)
		if err != nil {
			return err
		}
		d, _ := dsn(cfg.PublicURL, p)
		_, err = fmt.Fprintln(out, d)
		return err
	case args[0] == "list" && len(args) == 1:
		ps, err := db.ListProjects(ctx)
		if err != nil {
			return err
		}
		tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "ID\tNAME\tDSN")
		for _, p := range ps {
			d, _ := dsn(cfg.PublicURL, p)
			_, _ = fmt.Fprintf(tw, "%d\t%s\t%s\n", p.ID, p.Name, d)
		}
		return tw.Flush()
	case args[0] == "labels" && (len(args) == 2 || len(args) == 3):
		return labelsCommand(ctx, db, args[1:], out)
	case args[0] == "filters" && (len(args) == 2 || len(args) >= 4 && args[2] == "set"):
		return filtersCommand(ctx, db, args[1:], out)
	case args[0] == "dropped" && (len(args) == 2 || len(args) == 3):
		return droppedCommand(ctx, db, args[1:], out)
	case args[0] == "rules" && len(args) >= 2:
		alerts, err := alert.New(db, log, cfg.Alerts)
		if err != nil {
			return err
		}
		return rulesCommand(ctx, db, alerts, args[1:], os.Stdin, out)
	default:
		return errors.New(projectsUsage)
	}
}

func newPublicKey() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func dsn(publicURL string, p store.Project) (string, error) {
	if publicURL == "" {
		return "", errors.New("PUBLIC_URL is needed to write the DSN")
	}
	u, err := url.Parse(publicURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", fmt.Errorf("invalid PUBLIC_URL: %q", publicURL)
	}
	return fmt.Sprintf("%s://%s@%s/%d", u.Scheme, p.PublicKey, u.Host, p.ID), nil
}

// labelsCommand enseña, sustituye o quita las etiquetas de Linear propias de
// un proyecto: args es <nombre> [<id>[,<id>…] | --clear].
func labelsCommand(ctx context.Context, db *store.Store, args []string, out io.Writer) error {
	name := args[0]
	notFound := func(err error) error {
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("there is no project named %q", name)
		}
		return err
	}
	if len(args) == 1 {
		p, err := db.ProjectByName(ctx, name)
		if err != nil {
			return notFound(err)
		}
		if len(p.LinearLabelIDs) == 0 {
			_, err = fmt.Fprintf(out, "Linear issues from %q have no labels of their own, only those in LINEAR_LABEL_IDS\n", name)
			return err
		}
		_, err = fmt.Fprintln(out, strings.Join(p.LinearLabelIDs, "\n"))
		return err
	}

	var ids []string
	if args[1] != "--clear" {
		var err error
		if ids, err = parseLabelIDs(args[1]); err != nil {
			return err
		}
	}
	if err := db.SetProjectLabels(ctx, name, ids); err != nil {
		return notFound(err)
	}
	var err error
	if len(ids) == 0 {
		_, err = fmt.Fprintf(out, "Linear issues from %q no longer have labels of their own\n", name)
	} else {
		_, err = fmt.Fprintf(out, "Linear issues from %q also get: %s\n", name, strings.Join(ids, ", "))
	}
	return err
}

func parseLabelIDs(s string) ([]string, error) {
	var ids []string
	for _, id := range strings.Split(s, ",") {
		id = strings.TrimSpace(id)
		if id == "" {
			return nil, fmt.Errorf("empty label id in %q", s)
		}
		if strings.HasPrefix(id, "-") || strings.ContainsAny(id, " \t") {
			return nil, fmt.Errorf("invalid label id: %q", id)
		}
		if !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	return ids, nil
}
