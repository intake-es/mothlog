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

	"github.com/intake-es/mothlog/internal/api"
	"github.com/intake-es/mothlog/internal/store"
)

const rulesUsage = `usage of projects rules <name>:
  (nothing else)                 lists the alert rules
  show <id>                      shows one as JSON, with secrets masked
  create <file.json|->           creates one from the API JSON
  edit <id> <file.json|->        replaces it; empty or masked secrets don't change
  delete <id>                    deletes it
  test <id> [<action>]           sends a test alert through one action, or through all of them`

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
			return 0, fmt.Errorf("%q is not a rule id", args[1])
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
		_, err = fmt.Fprintf(out, "rule %d deleted\n", id)
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
				_, _ = fmt.Fprintf(out, "%s (%s): sent\n", a.ID, a.Type)
			}
		}
		if failed {
			return errors.New("some test alert failed")
		}
		return nil
	}
	return errors.New(rulesUsage)
}

func ruleNotFound(err error, id int64) error {
	if errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("the project has no rule %d", id)
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
		return r, fmt.Errorf("the rule is not valid JSON: %w", err)
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
		_, err := fmt.Fprintln(out, "\nSave the webhook secret now: it won't be shown in full again.")
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
		_, err := fmt.Fprintf(out, "%q has no rules: it alerts on new errors and regressions with SLACK_WEBHOOK_URL and LINEAR_API_KEY\n", p.Name)
		return err
	}
	tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "ID\tNAME\tACTIVE\tCONDITIONS\tACTIONS\tEVERY")
	for _, r := range rules {
		var acts []string
		for _, a := range r.Actions {
			acts = append(acts, a.Type+":"+a.ID)
		}
		active := "yes"
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
		parts = append(parts, "new")
	}
	if c.Regressed {
		parts = append(parts, "regression")
	}
	if f := c.Frequency; f != nil {
		parts = append(parts, fmt.Sprintf(">%d events/%d min", f.Events, f.WindowMinutes))
	}
	if u := c.Users; u != nil {
		parts = append(parts, fmt.Sprintf(">%d users", u.Users))
	}
	s := strings.Join(parts, " or ")
	var filters []string
	if len(c.Environments) > 0 {
		filters = append(filters, "environment "+strings.Join(c.Environments, "|"))
	}
	if len(c.Levels) > 0 {
		filters = append(filters, "level "+strings.Join(c.Levels, "|"))
	}
	for _, t := range c.Tags {
		filters = append(filters, "tag "+t.Key+"="+t.Value)
	}
	if len(filters) > 0 {
		s += "; " + strings.Join(filters, ", ")
	}
	return s
}
