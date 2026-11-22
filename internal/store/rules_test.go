package store

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestRulesCRUD(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	p, err := s.CreateProject(ctx, "web", randomHex(t, 16))
	if err != nil {
		t.Fatal(err)
	}
	rules, err := s.Rules(ctx, p.ID)
	if err != nil || len(rules) != 0 {
		t.Fatalf("sin reglas = %v, %v", rules, err)
	}

	in := Rule{
		Name: "  Pagos  ", Enabled: true, ThrottleMinutes: 30,
		Conditions: RuleConditions{New: true, Frequency: &FrequencyCondition{Events: 10, WindowMinutes: 5},
			Environments: []string{" production ", "production", ""}, Levels: []string{"error"},
			Tags: []TagFilter{{Key: " area ", Value: " pagos "}}},
		Actions: []RuleAction{
			{Type: ActionSlack, URL: "https://hooks.slack.test/services/T000/B000/abcdefghijkl", TeamID: "se ignora"},
			{Type: ActionWebhook, URL: "https://agente.test/hook"},
			{ID: "correo", Type: ActionEmail, To: []string{"guardia@errores.test"}},
			{Type: ActionNtfy, URL: "https://ntfy.sh/errores-backend-7f3a9c", Token: "tk_abcdefghijklmnop"},
		},
	}
	r, generated, err := s.CreateRule(ctx, p.ID, in)
	if err != nil {
		t.Fatal(err)
	}
	if r.Name != "Pagos" || r.ID == 0 || r.ProjectID != p.ID || !slices.Equal(r.Conditions.Environments, []string{"production"}) ||
		r.Conditions.Tags[0] != (TagFilter{Key: "area", Value: "pagos"}) || r.Actions[0].TeamID != "" || r.Actions[2].ID != "correo" {
		t.Fatalf("guardada = %+v", r)
	}
	hook := r.Actions[1]
	if len(generated) != 1 || generated[0] != hook.ID || len(hook.Secret) != 64 {
		t.Fatalf("secreto generado = %v, %+v", generated, hook)
	}

	masked := r.Masked()
	if got := masked.Actions[0].URL; got != "https://hooks.slack.test/…ijkl" {
		t.Errorf("slack tapado = %q", got)
	}
	if got := masked.Actions[1].Secret; !strings.HasPrefix(got, "…") || len(got) > 10 || masked.Actions[1].URL != "https://agente.test/hook" {
		t.Errorf("webhook tapado = %+v", masked.Actions[1])
	}
	if got := masked.Actions[3]; got.URL != "https://ntfy.sh/…3a9c" || got.Token != "…mnop" {
		t.Errorf("ntfy tapado = %+v", got)
	}
	if r.Actions[0].URL == masked.Actions[0].URL {
		t.Error("Masked cambia la regla original")
	}
	if shown := r.Masked(hook.ID); shown.Actions[1].Secret != hook.Secret {
		t.Error("Masked con el id no enseña el secreto")
	}

	// Al editar con lo que devolvió la API, los secretos tapados se conservan.
	edit := masked
	edit.Name = "Pagos y cobros"
	edit.Actions[1].Secret = ""
	got, generated, err := s.UpdateRule(ctx, p.ID, edit)
	if err != nil {
		t.Fatal(err)
	}
	if len(generated) != 0 || got.Actions[0].URL != in.Actions[0].URL || got.Actions[1].Secret != hook.Secret || got.Name != "Pagos y cobros" ||
		got.Actions[3].URL != in.Actions[3].URL {
		t.Fatalf("editada = %+v, %v", got, generated)
	}
	edit.Actions[0].URL = "https://hooks.slack.test/services/otro"
	if got, _, _ = s.UpdateRule(ctx, p.ID, edit); got.Actions[0].URL != "https://hooks.slack.test/services/otro" {
		t.Errorf("un secreto nuevo no se guarda: %q", got.Actions[0].URL)
	}
	// Un secreto tapado en una acción nueva no se puede guardar.
	edit.Actions = append(edit.Actions, RuleAction{Type: ActionDiscord, URL: "https://discord.test/…abcd"})
	if _, _, err := s.UpdateRule(ctx, p.ID, edit); !errors.Is(err, ErrInvalid) {
		t.Errorf("secreto tapado nuevo = %v", err)
	}

	other, err := s.CreateProject(ctx, "otro", randomHex(t, 16))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Rule(ctx, other.ID, r.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("la regla se ve desde otro proyecto: %v", err)
	}
	if _, _, err := s.UpdateRule(ctx, other.ID, edit); !errors.Is(err, ErrNotFound) {
		t.Errorf("se edita desde otro proyecto: %v", err)
	}
	if err := s.DeleteRule(ctx, other.ID, r.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("se borra desde otro proyecto: %v", err)
	}
	if _, _, err := s.CreateRule(ctx, p.ID+1000, in); !errors.Is(err, ErrNotFound) {
		t.Errorf("proyecto que no existe: %v", err)
	}
	if err := s.DeleteRule(ctx, p.ID, r.ID); err != nil {
		t.Fatal(err)
	}
	if rules, _ := s.Rules(ctx, p.ID); len(rules) != 0 {
		t.Errorf("tras borrar = %v", rules)
	}
}

func TestRuleValidation(t *testing.T) {
	base := func() Rule {
		return Rule{Name: "r", Conditions: RuleConditions{New: true},
			Actions: []RuleAction{{Type: ActionNtfy, URL: "https://ntfy.test/tema"}}}
	}
	cases := map[string]struct {
		change func(*Rule)
		want   string
	}{
		"sin nombre":         {func(r *Rule) { r.Name = " " }, "nombre"},
		"sin condición":      {func(r *Rule) { r.Conditions.New = false }, "al menos una condición"},
		"sin acciones":       {func(r *Rule) { r.Actions = nil }, "al menos una acción"},
		"ventana enorme":     {func(r *Rule) { r.Conditions.Frequency = &FrequencyCondition{Events: 1, WindowMinutes: 2000} }, "window_minutes"},
		"cero eventos":       {func(r *Rule) { r.Conditions.Frequency = &FrequencyCondition{Events: 0, WindowMinutes: 5} }, "frequency.events"},
		"cero usuarios":      {func(r *Rule) { r.Conditions.Users = &UsersCondition{} }, "users.users"},
		"nivel raro":         {func(r *Rule) { r.Conditions.Levels = []string{"critical"} }, "nivel"},
		"etiqueta sin clave": {func(r *Rule) { r.Conditions.Tags = []TagFilter{{Value: "x"}} }, "etiqueta"},
		"límite negativo":    {func(r *Rule) { r.ThrottleMinutes = -1 }, "throttle_minutes"},
		"tipo raro":          {func(r *Rule) { r.Actions[0].Type = "sms" }, "no existe"},
		"url no http":        {func(r *Rule) { r.Actions[0].URL = "ftp://x" }, "http"},
		"slack sin url":      {func(r *Rule) { r.Actions[0] = RuleAction{Type: ActionSlack} }, "falta url"},
		"correo malo":        {func(r *Rule) { r.Actions[0] = RuleAction{Type: ActionEmail, To: []string{"no es"}} }, "correo"},
		"correo vacío":       {func(r *Rule) { r.Actions[0] = RuleAction{Type: ActionEmail} }, "falta to"},
		"telegram sin chat":  {func(r *Rule) { r.Actions[0] = RuleAction{Type: ActionTelegram, BotToken: "1:x"} }, "chat_id"},
		"token raro":         {func(r *Rule) { r.Actions[0] = RuleAction{Type: ActionTelegram, BotToken: "1/x", ChatID: "1"} }, "bot_token"},
		"ids repetidos": {func(r *Rule) {
			r.Actions = []RuleAction{{ID: "a", Type: ActionNtfy, URL: "https://n.test/a"}, {ID: "a", Type: ActionNtfy, URL: "https://n.test/b"}}
		}, "repetido"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			r := base()
			c.change(&r)
			if _, err := r.Normalize(); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), c.want) {
				t.Errorf("err = %v; quiero %q", err, c.want)
			}
		})
	}
	r := base()
	if _, err := r.Normalize(); err != nil || r.Conditions.Levels == nil || r.Conditions.Tags == nil || r.Actions[0].ID == "" {
		t.Errorf("válida = %+v, %v", r, err)
	}
}
