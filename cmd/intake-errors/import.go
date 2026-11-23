package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/intake-es/intake-errors/internal/ingest"
	"github.com/intake-es/intake-errors/internal/sentryimport"
	"github.com/intake-es/intake-errors/internal/store"
)

const importUsage = "uso: intake-errors import sentry --url <https://sentry.io> --org <slug> [--project <slug>…]" +
	" [--since <fecha>] [--events <N>] [--dry-run]\n  con el token de la organización en SENTRY_IMPORT_TOKEN"

type importArgs struct {
	url, org string
	projects []string
	since    time.Time
	events   int
	dryRun   bool
}

func parseImportArgs(args []string) (importArgs, error) {
	if len(args) == 0 || args[0] != "sentry" {
		return importArgs{}, errors.New(importUsage)
	}
	for _, a := range args[1:] {
		if name := strings.TrimLeft(strings.SplitN(a, "=", 2)[0], "-"); a != "" && a[0] == '-' && strings.EqualFold(name, "token") {
			return importArgs{}, errors.New("el token no va en la línea de órdenes: ponlo en SENTRY_IMPORT_TOKEN")
		}
	}
	var a importArgs
	var since string
	fs := flag.NewFlagSet("import sentry", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&a.url, "url", "", "")
	fs.StringVar(&a.org, "org", "", "")
	fs.Func("project", "", func(v string) error {
		for p := range strings.SplitSeq(v, ",") {
			if p = strings.TrimSpace(p); p != "" {
				a.projects = append(a.projects, p)
			}
		}
		return nil
	})
	fs.StringVar(&since, "since", "", "")
	fs.IntVar(&a.events, "events", sentryimport.DefaultEvents, "")
	fs.BoolVar(&a.dryRun, "dry-run", false, "")
	if err := fs.Parse(args[1:]); err != nil {
		return a, fmt.Errorf("%v\n%s", err, importUsage)
	}
	switch {
	case fs.NArg() > 0:
		return a, fmt.Errorf("sobra %q\n%s", fs.Arg(0), importUsage)
	case a.url == "" || a.org == "":
		return a, fmt.Errorf("faltan --url y --org\n%s", importUsage)
	case a.events < 1:
		return a, errors.New("--events tiene que ser al menos 1: un issue sin eventos lo borra la retención")
	}
	if since != "" {
		var err error
		if a.since, err = time.Parse(time.RFC3339, since); err != nil {
			if a.since, err = time.Parse(time.DateOnly, since); err != nil {
				return a, fmt.Errorf("--since: %q no es una fecha como 2024-05-31 o 2024-05-31T10:00:00Z", since)
			}
		}
	}
	return a, nil
}

func importCommand(cfg config, args []string, out, progress io.Writer) error {
	a, err := parseImportArgs(args)
	if err != nil {
		return err
	}
	client, err := sentryimport.NewClient(a.url, os.Getenv("SENTRY_IMPORT_TOKEN"), nil)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	log := slog.New(slog.NewTextHandler(progress, &slog.HandlerOptions{Level: max(cfg.LogLevel, slog.LevelWarn)}))
	db, err := store.Open(ctx, cfg.DatabaseURL, log)
	if err != nil {
		return err
	}
	defer db.Close()
	im := sentryimport.New(client, db, ingest.NewIngester(db.Pool(), nil, nil, log), sentryimport.Options{
		Org: a.org, Projects: a.projects, Since: a.since, Events: a.events, DryRun: a.dryRun, Progress: progress,
	})
	rep, err := im.Run(ctx)
	if rep != nil {
		if werr := rep.Write(out); werr != nil && err == nil {
			err = werr
		}
	}
	return err
}
