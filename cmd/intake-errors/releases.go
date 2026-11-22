package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/intake-es/intake-errors/internal/store"
)

const releasesUsage = "uso: intake-errors releases new <proyecto> <versión> [--env <entorno>]"

// releasesCommand crea una release a mano y, con --env, apunta su despliegue
// en ese entorno.
func releasesCommand(ctx context.Context, db *store.Store, args []string, out io.Writer) error {
	if len(args) < 3 || args[0] != "new" {
		return errors.New(releasesUsage)
	}
	project, version, env := args[1], args[2], ""
	switch rest := args[3:]; {
	case len(rest) == 2 && rest[0] == "--env":
		env = rest[1]
	case len(rest) == 1 && strings.HasPrefix(rest[0], "--env="):
		env = strings.TrimPrefix(rest[0], "--env=")
	case len(rest) != 0:
		return errors.New(releasesUsage)
	}
	p, err := projectByName(ctx, db, project)
	if err != nil {
		return err
	}
	rel, created, err := db.CreateRelease(ctx, p.ID, version)
	if err != nil {
		return err
	}
	if created {
		_, err = fmt.Fprintf(out, "release %s creada en %s\n", rel.Version, p.Name)
	} else {
		_, err = fmt.Fprintf(out, "la release %s ya existía en %s\n", rel.Version, p.Name)
	}
	if err != nil || env == "" {
		return err
	}
	d, resolved, err := db.AddDeploy(ctx, p.ID, rel.Version, env, time.Now())
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "desplegada en %s el %s; %d issues resueltos con ella\n", d.Environment, day(&d.DeployedAt), len(resolved))
	return err
}
