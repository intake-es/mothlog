package sentryimport

import (
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/intake-es/intake-errors/internal/store"
)

// Counts es cuántas cosas de un tipo se han creado, actualizado, dejado como
// estaban o saltado (por --since, porque no se entienden o porque fallaron).
type Counts struct {
	Created   int `json:"created"`
	Updated   int `json:"updated"`
	Unchanged int `json:"unchanged"`
	Skipped   int `json:"skipped"`
}

func (c *Counts) add(a store.ImportAction) {
	switch a {
	case store.ImportCreated:
		c.Created++
	case store.ImportUpdated:
		c.Updated++
	default:
		c.Unchanged++
	}
}

type Report struct {
	Server   string `json:"server"`
	Org      string `json:"org"`
	DryRun   bool   `json:"dry_run"`
	Projects Counts `json:"projects"`
	Releases Counts `json:"releases"`
	Deploys  Counts `json:"deploys"`
	Issues   Counts `json:"issues"`
	Events   Counts `json:"events"`
	// NotGrouped son los issues cuyos eventos nuevos de la ingesta no irán a
	// ellos, y por qué.
	NotGrouped []string `json:"not_grouped"`
	// Skipped son los saltados que merecen explicación.
	Skipped []string `json:"skipped"`
	// Errors son los errores de la API y de lo que no se ha podido guardar.
	Errors []string `json:"errors"`
}

func (r *Report) errorf(format string, args ...any) {
	r.Errors = append(r.Errors, fmt.Sprintf(format, args...))
}

func (r *Report) skip(format string, args ...any) {
	r.Skipped = append(r.Skipped, fmt.Sprintf(format, args...))
}

// Write escribe el informe para leerlo en la terminal.
func (r *Report) Write(w io.Writer) error {
	head := fmt.Sprintf("importado de %s, organización %s", r.Server, r.Org)
	cols := "CREADOS\tACTUALIZADOS\tSIN CAMBIOS\tSALTADOS"
	if r.DryRun {
		head = fmt.Sprintf("prueba (--dry-run) de importar de %s, organización %s: no se ha escrito nada", r.Server, r.Org)
		cols = "SE CREARÍAN\tSE ACTUALIZARÍAN\tSIN CAMBIOS\tSE SALTARÍAN"
	}
	if _, err := fmt.Fprintf(w, "\n%s\n\n", head); err != nil {
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintf(tw, "\t%s\t\n", cols)
	for _, row := range []struct {
		name string
		c    Counts
	}{
		{"proyectos", r.Projects}, {"releases", r.Releases}, {"despliegues", r.Deploys},
		{"issues", r.Issues}, {"eventos", r.Events},
	} {
		_, _ = fmt.Fprintf(tw, "%s\t%d\t%d\t%d\t%d\t\n", row.name, row.c.Created, row.c.Updated, row.c.Unchanged, row.c.Skipped)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	for _, sec := range []struct {
		title string
		lines []string
	}{
		{"issues que no se agrupan con los eventos nuevos de la ingesta", r.NotGrouped},
		{"saltados", r.Skipped},
		{"errores", r.Errors},
	} {
		if len(sec.lines) == 0 {
			continue
		}
		_, _ = fmt.Fprintf(w, "\n%s (%d):\n", sec.title, len(sec.lines))
		for _, l := range sec.lines {
			_, _ = fmt.Fprintf(w, "  %s\n", l)
		}
	}
	if len(r.Errors) == 0 {
		_, err := fmt.Fprintln(w, "\nsin errores")
		return err
	}
	return nil
}
