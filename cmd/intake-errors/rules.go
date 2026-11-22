package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/intake-es/intake-errors/internal/api"
	"github.com/intake-es/intake-errors/internal/store"
)

const rulesUsage = `uso de projects rules <nombre>:
  (sin más)                      lista las reglas de aviso
  show <id>                      enseña una en JSON, con los secretos tapados
  create <fichero.json|->        crea una con el JSON de la API
  edit <id> <fichero.json|->     la sustituye; los secretos vacíos o tapados no cambian
  delete <id>                    la borra
  test <id> [<acción>]           manda un aviso de prueba por una acción, o por todas`

// rulesCommand gestiona las reglas de aviso de un proyecto: args es <nombre> [<orden> …].
func rulesCommand(ctx context.Context, db *store.Store, alerts api.Alerts, args []string, in io.Reader, out io.Writer) error {
	p, err := projectByName(ctx, db, args[0])
	if err != nil {
		return err
	}
	args = args[1:]
	if len(args) == 0 {
		return listRules(ctx, db, p, out)
	}
	ruleID := func() (int64, error) {
		id, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil || id <= 0 {
			return 0, fmt.Errorf("%q no es el id de una regla", args[1])
		}
		return id, nil
	}
	switch {
	case args[0] == "show" && len(args) == 2:
		id, err := ruleID()
		if err != nil {
			return err
		}
		r, err := db.Rule(ctx, p.ID, id)
		if err != nil {
			return ruleNotFound(err, id)
		}
		return printRule(out, r.Masked())
	case args[0] == "create" && len(args) == 2:
		r, err := readRule(args[1], in)
		if err != nil {
			return err
		}
		if err := alerts.CheckRule(r); err != nil {
			return err
		}
		r, generated, err := db.CreateRule(ctx, p.ID, r)
		if err != nil {
			return err
		}
		return printSaved(out, r, generated)
	case args[0] == "edit" && len(args) == 3:
		id, err := ruleID()
		if err != nil {
			return err
		}
		r, err := readRule(args[2], in)
		if err != nil {
			return err
		}
		r.ID = id
		if err := alerts.CheckRule(r); err != nil {
			return err
		}
		r, generated, err := db.UpdateRule(ctx, p.ID, r)
		if err != nil {
			return ruleNotFound(err, id)
		}
		return printSaved(out, r, generated)
	case args[0] == "delete" && len(args) == 2:
		id, err := ruleID()
		if err != nil {
			return err
		}
		if err := db.DeleteRule(ctx, p.ID, id); err != nil {
			return ruleNotFound(err, id)
		}
		_, err = fmt.Fprintf(out, "regla %d borrada\n", id)
		return err
	case args[0] == "test" && (len(args) == 2 || len(args) == 3):
		id, err := ruleID()
		if err != nil {
			return err
		}
		r, err := db.Rule(ctx, p.ID, id)
		if err != nil {
			return ruleNotFound(err, id)
		}
		var failed bool
		for _, a := range r.Actions {
			if len(args) == 3 && a.ID != args[2] {
				continue
			}
			if err := alerts.TestAction(ctx, r, a.ID); err != nil {
				failed = true
				_, _ = fmt.Fprintf(out, "%s (%s): %v\n", a.ID, a.Type, err)
			} else {
				_, _ = fmt.Fprintf(out, "%s (%s): enviado\n", a.ID, a.Type)
			}
		}
		if failed {
			return errors.New("algún aviso de prueba ha fallado")
		}
		return nil
	}
	return errors.New(rulesUsage)
}

func ruleNotFound(err error, id int64) error {
	if errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("el proyecto no tiene ninguna regla %d", id)
	}
	return err
}

func readRule(path string, in io.Reader) (store.Rule, error) {
	r := store.Rule{Enabled: true}
	var raw []byte
	var err error
	if path == "-" {
		raw, err = io.ReadAll(io.LimitReader(in, 1<<20))
	} else {
		raw, err = os.ReadFile(path)
	}
	if err != nil {
		return r, err
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return r, fmt.Errorf("la regla no es JSON válido: %w", err)
	}
	return r, nil
}

func printRule(out io.Writer, r store.Rule) error {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(r)
}

func printSaved(out io.Writer, r store.Rule, generated []string) error {
	if err := printRule(out, r.Masked(generated...)); err != nil {
		return err
	}
	if len(generated) > 0 {
		_, err := fmt.Fprintln(out, "\nGuarda el secreto del webhook ahora: no se vuelve a mostrar entero.")
		return err
	}
	return nil
}

func listRules(ctx context.Context, db *store.Store, p store.Project, out io.Writer) error {
	rules, err := db.Rules(ctx, p.ID)
	if err != nil {
		return err
	}
	if len(rules) == 0 {
		_, err := fmt.Fprintf(out, "%q no tiene reglas: avisa de los errores nuevos y las regresiones con SLACK_WEBHOOK_URL y LINEAR_API_KEY\n", p.Name)
		return err
	}
	tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "ID\tNOMBRE\tACTIVA\tCONDICIONES\tACCIONES\tCADA")
	for _, r := range rules {
		var acts []string
		for _, a := range r.Actions {
			acts = append(acts, a.Type+":"+a.ID)
		}
		active := "sí"
		if !r.Enabled {
			active = "no"
		}
		every := "-"
		if r.ThrottleMinutes > 0 {
			every = strconv.Itoa(r.ThrottleMinutes) + " min"
		}
		_, _ = fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\n", r.ID, r.Name, active, conditionsText(r.Conditions), strings.Join(acts, ", "), every)
	}
	return tw.Flush()
}

func conditionsText(c store.RuleConditions) string {
	var parts []string
	if c.New {
		parts = append(parts, "nuevo")
	}
	if c.Regressed {
		parts = append(parts, "regresión")
	}
	if f := c.Frequency; f != nil {
		parts = append(parts, fmt.Sprintf(">%d eventos/%d min", f.Events, f.WindowMinutes))
	}
	if u := c.Users; u != nil {
		parts = append(parts, fmt.Sprintf(">%d usuarios", u.Users))
	}
	s := strings.Join(parts, " o ")
	var filters []string
	if len(c.Environments) > 0 {
		filters = append(filters, "entorno "+strings.Join(c.Environments, "|"))
	}
	if len(c.Levels) > 0 {
		filters = append(filters, "nivel "+strings.Join(c.Levels, "|"))
	}
	for _, t := range c.Tags {
		filters = append(filters, "etiqueta "+t.Key+"="+t.Value)
	}
	if len(filters) > 0 {
		s += "; " + strings.Join(filters, ", ")
	}
	return s
}
