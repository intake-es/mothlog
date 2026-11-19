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
	"strings"
	"text/tabwriter"

	"github.com/intake-es/intake-errors/internal/store"
)

const projectsUsage = "uso: intake-errors projects create <nombre> | projects list | projects extra-label <nombre> on|off"

func projects(cfg config, args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New(projectsUsage)
	}
	if args[0] != "extra-label" {
		if _, err := dsn(cfg.PublicURL, store.Project{}); err != nil {
			return err
		}
	}
	ctx := context.Background()
	// Los logs de las migraciones van a stderr para no mezclarse con la salida.
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: cfg.LogLevel}))
	db, err := store.Open(ctx, cfg.DatabaseURL, log)
	if err != nil {
		return err
	}
	defer db.Close()

	switch {
	case args[0] == "create" && len(args) == 2:
		name := strings.TrimSpace(args[1])
		if name == "" {
			return errors.New("el nombre no puede estar vacío")
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
		_, _ = fmt.Fprintln(tw, "ID\tNOMBRE\tDSN")
		for _, p := range ps {
			d, _ := dsn(cfg.PublicURL, p)
			_, _ = fmt.Fprintf(tw, "%d\t%s\t%s\n", p.ID, p.Name, d)
		}
		return tw.Flush()
	case args[0] == "extra-label" && len(args) == 3:
		return extraLabelCommand(ctx, db, args[1], args[2], out)
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
		return "", errors.New("falta PUBLIC_URL para escribir el DSN")
	}
	u, err := url.Parse(publicURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", fmt.Errorf("PUBLIC_URL no válida: %q", publicURL)
	}
	return fmt.Sprintf("%s://%s@%s/%d", u.Scheme, p.PublicKey, u.Host, p.ID), nil
}

func extraLabelCommand(ctx context.Context, db *store.Store, name, state string, out io.Writer) error {
	var on bool
	switch state {
	case "on":
		on = true
	case "off":
	default:
		return errors.New(projectsUsage)
	}
	err := db.SetProjectExtraLabel(ctx, name, on)
	if errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("no hay ningún proyecto llamado %q", name)
	}
	if err != nil {
		return err
	}
	if on {
		_, err = fmt.Fprintf(out, "las issues de Linear de %q llevan la etiqueta extra\n", name)
	} else {
		_, err = fmt.Fprintf(out, "las issues de Linear de %q ya no llevan la etiqueta extra\n", name)
	}
	return err
}
