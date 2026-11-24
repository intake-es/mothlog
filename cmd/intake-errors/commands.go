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

	"github.com/intake-es/intake-errors/internal/store"
)

const usage = `uso:
  intake-errors                        arranca el servicio
  intake-errors projects create <nombre> crea un proyecto e imprime su DSN
  intake-errors projects list           lista los proyectos y sus DSN
  intake-errors projects labels <nombre> enseña las etiquetas de Linear propias del proyecto
  intake-errors projects labels <nombre> <id>[,<id>…]  las sustituye
  intake-errors projects labels <nombre> --clear  las quita
  intake-errors projects filters <nombre>  enseña sus filtros, el tope por issue y el muestreo
  intake-errors projects filters <nombre> set <clave> [<valor>…]  cambia uno
  intake-errors projects dropped <nombre> [<días>]  cuenta lo descartado por motivo (30 días por defecto)
  intake-errors releases new <proyecto> <versión> [--env <entorno>]  crea la release y, con --env, apunta un despliegue
  intake-errors projects rules <nombre> [<orden> …]  lista, crea, edita, borra y prueba sus reglas de aviso
  intake-errors tokens create <nombre>  crea un token de la API y lo imprime una sola vez
  intake-errors tokens list             lista los tokens
  intake-errors tokens revoke <nombre>  revoca el token activo con ese nombre
  intake-errors people list             lista quién ha entrado en la interfaz
  intake-errors people revoke <login>   cierra todas las sesiones de esa persona y revoca sus aplicaciones conectadas al MCP (para darla de baja)
  intake-errors import sentry --url <servidor> --org <slug> [--project <slug>…] [--since <fecha>] [--events <N>] [--dry-run]
                                        importa de Sentry, con el token en SENTRY_IMPORT_TOKEN
  intake-errors version                 imprime la versión, el commit y la fecha del binario`

func runCommand(args []string, out io.Writer) error {
	if args[0] != "tokens" && args[0] != "releases" && args[0] != "people" {
		return fmt.Errorf("orden desconocida %q\n%s", args[0], usage)
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
		_, err = fmt.Fprintf(out, "%s\n\nGuárdalo ahora: no se vuelve a mostrar.\n", secret)
		return err
	case len(args) == 1 && args[0] == "list":
		tokens, err := db.ListTokens(ctx)
		if err != nil {
			return err
		}
		tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "NOMBRE\tALTA\tÚLTIMO USO\tREVOCADO")
		for _, t := range tokens {
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", t.Name, day(&t.CreatedAt), day(t.LastUsedAt), day(t.RevokedAt))
		}
		return tw.Flush()
	case len(args) == 2 && args[0] == "revoke":
		err := db.RevokeToken(ctx, args[1])
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("no hay ningún token activo llamado %q", args[1])
		}
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(out, "token %q revocado\n", args[1])
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
