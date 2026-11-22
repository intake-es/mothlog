package store

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Tipos de acción de una regla de aviso.
const (
	ActionSlack    = "slack"
	ActionLinear   = "linear"
	ActionEmail    = "email"
	ActionDiscord  = "discord"
	ActionTelegram = "telegram"
	ActionNtfy     = "ntfy"
	ActionWebhook  = "webhook"
)

var ActionTypes = []string{ActionSlack, ActionLinear, ActionEmail, ActionDiscord, ActionTelegram, ActionNtfy, ActionWebhook}

var Levels = []string{"fatal", "error", "warning", "info", "debug"}

const (
	maxRuleName        = 200
	maxRuleActions     = 10
	maxRuleListItems   = 50
	maxRuleValue       = 2000
	MaxWindowMinutes   = 24 * 60
	MaxThrottleMinutes = 7 * 24 * 60
	maxThreshold       = 1_000_000
	secretMask         = "…"
)

// Rule dice cuándo avisa un proyecto y a dónde.
type Rule struct {
	ID         int64          `json:"id"`
	ProjectID  int64          `json:"project_id"`
	Name       string         `json:"name"`
	Enabled    bool           `json:"enabled"`
	Conditions RuleConditions `json:"conditions"`
	Actions    []RuleAction   `json:"actions"`
	// ThrottleMinutes es lo menos que pasa entre dos avisos de la regla por el mismo issue.
	ThrottleMinutes int       `json:"throttle_minutes"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// RuleConditions: la regla salta si se cumple alguno de los disparadores
// (New, Regressed, Frequency, Users) y todos los filtros.
type RuleConditions struct {
	New       bool                `json:"new"`
	Regressed bool                `json:"regressed"`
	Frequency *FrequencyCondition `json:"frequency"`
	Users     *UsersCondition     `json:"users"`
	// Environments, Levels y Tags vacíos no filtran.
	Environments []string    `json:"environments"`
	Levels       []string    `json:"levels"`
	Tags         []TagFilter `json:"tags"`
}

// FrequencyCondition salta cuando el issue pasa de Events eventos en los
// últimos WindowMinutes minutos.
type FrequencyCondition struct {
	Events        int `json:"events"`
	WindowMinutes int `json:"window_minutes"`
}

// UsersCondition salta cuando el issue pasa de Users usuarios distintos.
type UsersCondition struct {
	Users int `json:"users"`
}

// TagFilter pide la etiqueta Key con el valor Value; sin valor, basta con que esté.
type TagFilter struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// RuleAction es un sitio al que avisar; cada tipo usa sus campos.
type RuleAction struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	// URL es el webhook de Slack, Discord y el genérico, y el tema de ntfy.
	URL string `json:"url,omitempty"`
	// TeamID vacío es LINEAR_TEAM_ID; LabelIDs vacío, LINEAR_LABEL_IDS.
	TeamID   string   `json:"team_id,omitempty"`
	LabelIDs []string `json:"label_ids,omitempty"`
	To       []string `json:"to,omitempty"`
	BotToken string   `json:"bot_token,omitempty"`
	ChatID   string   `json:"chat_id,omitempty"`
	// Token es el de acceso a ntfy, si el tema lo pide.
	Token string `json:"token,omitempty"`
	// Secret firma el webhook genérico; vacío al crear, se genera uno.
	Secret string `json:"secret,omitempty"`
}

// secrets son los campos que no se devuelven enteros.
func (a *RuleAction) secrets() []*string {
	switch a.Type {
	case ActionSlack, ActionDiscord:
		return []*string{&a.URL}
	case ActionTelegram:
		return []*string{&a.BotToken}
	case ActionNtfy:
		// En un servidor público, el nombre del tema es lo que protege el canal.
		return []*string{&a.URL, &a.Token}
	case ActionWebhook:
		return []*string{&a.Secret}
	}
	return nil
}

// Mask tapa un secreto: de una URL deja el esquema y el host; del resto, los cuatro últimos caracteres.
func Mask(s string) string {
	if s == "" {
		return ""
	}
	tail := ""
	if r := []rune(s); len(r) > 12 {
		tail = string(r[len(r)-4:])
	}
	if u, err := url.Parse(s); err == nil && u.Scheme != "" && u.Host != "" {
		return u.Scheme + "://" + u.Host + "/" + secretMask + tail
	}
	return secretMask + tail
}

// Masked devuelve la regla con los secretos tapados, salvo los de las acciones de reveal.
func (r Rule) Masked(reveal ...string) Rule {
	r.Actions = slices.Clone(r.Actions)
	for i := range r.Actions {
		if slices.Contains(reveal, r.Actions[i].ID) {
			continue
		}
		for _, s := range r.Actions[i].secrets() {
			*s = Mask(*s)
		}
	}
	return r
}

// keepSecrets pone el secreto guardado en los campos que llegan tapados, como
// los devolvió Masked, o vacíos.
func (r *Rule) keepSecrets(old Rule) {
	for i := range r.Actions {
		a := &r.Actions[i]
		j := slices.IndexFunc(old.Actions, func(o RuleAction) bool { return o.ID == a.ID && o.Type == a.Type })
		if j < 0 {
			continue
		}
		o := old.Actions[j]
		olds := o.secrets()
		for k, s := range a.secrets() {
			if *s == "" || *s == Mask(*olds[k]) {
				*s = *olds[k]
			}
		}
	}
}

func hexID(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func cleanList(name string, in []string, check func(string) error) ([]string, error) {
	out := []string{}
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if len(s) > maxRuleValue {
			return nil, fmt.Errorf("%w: %s admite valores de hasta %d caracteres", ErrInvalid, name, maxRuleValue)
		}
		if check != nil {
			if err := check(s); err != nil {
				return nil, err
			}
		}
		if !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	if len(out) > maxRuleListItems {
		return nil, fmt.Errorf("%w: %s admite hasta %d valores", ErrInvalid, name, maxRuleListItems)
	}
	return out, nil
}

func checkURL(field string) func(string) error {
	return func(s string) error {
		u, err := url.Parse(s)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("%w: %s debe ser una URL http o https", ErrInvalid, field)
		}
		return nil
	}
}

// Normalize limpia la regla y comprueba que es válida. Da un id a las acciones
// que no lo tienen y un secreto a los webhooks que no lo traen, y devuelve los
// ids de las acciones con el secreto recién generado.
func (r *Rule) Normalize() ([]string, error) {
	r.Name = strings.TrimSpace(r.Name)
	switch {
	case r.Name == "":
		return nil, fmt.Errorf("%w: la regla necesita un nombre", ErrInvalid)
	case len([]rune(r.Name)) > maxRuleName:
		return nil, fmt.Errorf("%w: el nombre admite hasta %d caracteres", ErrInvalid, maxRuleName)
	case r.ThrottleMinutes < 0 || r.ThrottleMinutes > MaxThrottleMinutes:
		return nil, fmt.Errorf("%w: throttle_minutes va de 0 a %d", ErrInvalid, MaxThrottleMinutes)
	}

	c := &r.Conditions
	if !c.New && !c.Regressed && c.Frequency == nil && c.Users == nil {
		return nil, fmt.Errorf("%w: la regla necesita al menos una condición: new, regressed, frequency o users", ErrInvalid)
	}
	if f := c.Frequency; f != nil {
		if f.Events < 1 || f.Events > maxThreshold {
			return nil, fmt.Errorf("%w: frequency.events va de 1 a %d", ErrInvalid, maxThreshold)
		}
		if f.WindowMinutes < 1 || f.WindowMinutes > MaxWindowMinutes {
			return nil, fmt.Errorf("%w: frequency.window_minutes va de 1 a %d", ErrInvalid, MaxWindowMinutes)
		}
	}
	if u := c.Users; u != nil && (u.Users < 1 || u.Users > maxThreshold) {
		return nil, fmt.Errorf("%w: users.users va de 1 a %d", ErrInvalid, maxThreshold)
	}
	var err error
	if c.Environments, err = cleanList("environments", c.Environments, nil); err != nil {
		return nil, err
	}
	if c.Levels, err = cleanList("levels", c.Levels, func(s string) error {
		if !slices.Contains(Levels, s) {
			return fmt.Errorf("%w: el nivel %q no existe; son %s", ErrInvalid, s, strings.Join(Levels, ", "))
		}
		return nil
	}); err != nil {
		return nil, err
	}
	tags := []TagFilter{}
	for _, t := range c.Tags {
		t.Key, t.Value = strings.TrimSpace(t.Key), strings.TrimSpace(t.Value)
		if t.Key == "" {
			return nil, fmt.Errorf("%w: una etiqueta del filtro no tiene nombre", ErrInvalid)
		}
		if len(t.Key) > maxRuleValue || len(t.Value) > maxRuleValue {
			return nil, fmt.Errorf("%w: las etiquetas admiten hasta %d caracteres", ErrInvalid, maxRuleValue)
		}
		if !slices.Contains(tags, t) {
			tags = append(tags, t)
		}
	}
	if len(tags) > maxRuleListItems {
		return nil, fmt.Errorf("%w: tags admite hasta %d valores", ErrInvalid, maxRuleListItems)
	}
	c.Tags = tags

	if len(r.Actions) == 0 {
		return nil, fmt.Errorf("%w: la regla necesita al menos una acción", ErrInvalid)
	}
	if len(r.Actions) > maxRuleActions {
		return nil, fmt.Errorf("%w: la regla admite hasta %d acciones", ErrInvalid, maxRuleActions)
	}
	var generated []string
	seen := map[string]bool{}
	for i := range r.Actions {
		a := &r.Actions[i]
		a.ID = strings.TrimSpace(a.ID)
		if a.ID == "" {
			a.ID = hexID(4)
		}
		if seen[a.ID] || len(a.ID) > 64 {
			return nil, fmt.Errorf("%w: id de acción repetido o demasiado largo: %q", ErrInvalid, a.ID)
		}
		seen[a.ID] = true
		gen, err := a.normalize()
		if err != nil {
			return nil, fmt.Errorf("%w (acción %d, %s)", err, i+1, a.Type)
		}
		if gen {
			generated = append(generated, a.ID)
		}
	}
	return generated, nil
}

func (a *RuleAction) normalize() (bool, error) {
	for _, s := range []*string{&a.URL, &a.TeamID, &a.BotToken, &a.ChatID, &a.Token, &a.Secret} {
		*s = strings.TrimSpace(*s)
		if len(*s) > maxRuleValue {
			return false, fmt.Errorf("%w: los campos de una acción admiten hasta %d caracteres", ErrInvalid, maxRuleValue)
		}
	}
	for _, s := range a.secrets() {
		if strings.Contains(*s, secretMask) {
			return false, fmt.Errorf("%w: el secreto llega tapado; escríbelo entero", ErrInvalid)
		}
	}
	keep := RuleAction{ID: a.ID, Type: a.Type}
	var err error
	need := func(v, field string) {
		if err == nil && v == "" {
			err = fmt.Errorf("%w: falta %s", ErrInvalid, field)
		}
	}
	generated := false
	switch a.Type {
	case ActionSlack, ActionDiscord, ActionNtfy, ActionWebhook:
		need(a.URL, "url")
		if err == nil {
			err = checkURL("url")(a.URL)
		}
		keep.URL = a.URL
		switch a.Type {
		case ActionNtfy:
			keep.Token = a.Token
		case ActionWebhook:
			if a.Secret == "" {
				a.Secret, generated = hexID(32), true
			}
			keep.Secret = a.Secret
		}
	case ActionLinear:
		keep.TeamID = a.TeamID
		keep.LabelIDs, err = cleanList("label_ids", a.LabelIDs, nil)
	case ActionEmail:
		keep.To, err = cleanList("to", a.To, func(s string) error {
			if addr, err := mail.ParseAddress(s); err != nil || addr.Address != s {
				return fmt.Errorf("%w: %q no es una dirección de correo", ErrInvalid, s)
			}
			return nil
		})
		if err == nil && len(keep.To) == 0 {
			err = fmt.Errorf("%w: falta to, a quién mandar el correo", ErrInvalid)
		}
	case ActionTelegram:
		need(a.BotToken, "bot_token")
		need(a.ChatID, "chat_id")
		if err == nil && strings.ContainsAny(a.BotToken, "/?# ") {
			err = fmt.Errorf("%w: bot_token no es un token de bot", ErrInvalid)
		}
		keep.BotToken, keep.ChatID = a.BotToken, a.ChatID
	default:
		return false, fmt.Errorf("%w: el tipo de acción %q no existe; son %s", ErrInvalid, a.Type, strings.Join(ActionTypes, ", "))
	}
	*a = keep
	return generated, err
}

const ruleColumns = `id, project_id, name, enabled, conditions, actions, throttle_minutes, created_at, updated_at`

func scanRule(r pgx.Row) (Rule, error) {
	var ru Rule
	err := r.Scan(&ru.ID, &ru.ProjectID, &ru.Name, &ru.Enabled, &ru.Conditions, &ru.Actions,
		&ru.ThrottleMinutes, &ru.CreatedAt, &ru.UpdatedAt)
	return ru, err
}

// Rules da las reglas del proyecto, de la más antigua a la más nueva, con los secretos enteros.
func (s *Store) Rules(ctx context.Context, projectID int64) ([]Rule, error) {
	if _, err := s.ProjectByID(ctx, projectID); err != nil {
		return nil, err
	}
	return s.rules(ctx, s.pool, projectID)
}

type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func (s *Store) rules(ctx context.Context, q querier, projectID int64) ([]Rule, error) {
	rows, err := q.Query(ctx, `SELECT `+ruleColumns+` FROM alert_rules WHERE project_id = $1 ORDER BY id`, projectID)
	if err != nil {
		return nil, fmt.Errorf("leer reglas: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Rule, error) { return scanRule(r) })
	if err != nil {
		return nil, fmt.Errorf("leer reglas: %w", err)
	}
	return out, nil
}

// RulesOfIssue son las reglas del proyecto del issue, para evaluarlas dentro de la transacción de la ingesta.
func RulesOfIssue(ctx context.Context, tx pgx.Tx, issueID int64) ([]Rule, error) {
	rows, err := tx.Query(ctx, `SELECT `+ruleColumns+` FROM alert_rules
		WHERE project_id = (SELECT project_id FROM issues WHERE id = $1) ORDER BY id`, issueID)
	if err != nil {
		return nil, fmt.Errorf("leer reglas: %w", err)
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Rule, error) { return scanRule(r) })
}

func (s *Store) Rule(ctx context.Context, projectID, id int64) (Rule, error) {
	r, err := scanRule(s.pool.QueryRow(ctx, `SELECT `+ruleColumns+` FROM alert_rules WHERE project_id = $1 AND id = $2`, projectID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Rule{}, ErrNotFound
	}
	if err != nil {
		return Rule{}, fmt.Errorf("leer regla: %w", err)
	}
	return r, nil
}

// RuleByID busca una regla sin saber su proyecto.
func (s *Store) RuleByID(ctx context.Context, id int64) (Rule, error) {
	r, err := scanRule(s.pool.QueryRow(ctx, `SELECT `+ruleColumns+` FROM alert_rules WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Rule{}, ErrNotFound
	}
	if err != nil {
		return Rule{}, fmt.Errorf("leer regla: %w", err)
	}
	return r, nil
}

// CreateRule guarda una regla nueva en el proyecto y devuelve, además, las
// acciones con el secreto recién generado.
func (s *Store) CreateRule(ctx context.Context, projectID int64, r Rule) (Rule, []string, error) {
	generated, err := r.Normalize()
	if err != nil {
		return Rule{}, nil, err
	}
	out, err := scanRule(s.pool.QueryRow(ctx, `
		INSERT INTO alert_rules (project_id, name, enabled, conditions, actions, throttle_minutes)
		SELECT id, $2, $3, $4, $5, $6 FROM projects WHERE id = $1
		RETURNING `+ruleColumns, projectID, r.Name, r.Enabled, r.Conditions, r.Actions, r.ThrottleMinutes))
	if errors.Is(err, pgx.ErrNoRows) {
		return Rule{}, nil, ErrNotFound
	}
	if err != nil {
		return Rule{}, nil, fmt.Errorf("guardar regla: %w", err)
	}
	return out, generated, nil
}

// UpdateRule sustituye la regla. Los secretos que llegan vacíos o tapados se conservan.
func (s *Store) UpdateRule(ctx context.Context, projectID int64, r Rule) (Rule, []string, error) {
	old, err := s.Rule(ctx, projectID, r.ID)
	if err != nil {
		return Rule{}, nil, err
	}
	r.keepSecrets(old)
	generated, err := r.Normalize()
	if err != nil {
		return Rule{}, nil, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Rule{}, nil, fmt.Errorf("guardar regla: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	out, err := scanRule(tx.QueryRow(ctx, `
		UPDATE alert_rules SET name = $3, enabled = $4, conditions = $5, actions = $6, throttle_minutes = $7, updated_at = now()
		WHERE project_id = $1 AND id = $2
		RETURNING `+ruleColumns, projectID, r.ID, r.Name, r.Enabled, r.Conditions, r.Actions, r.ThrottleMinutes))
	if errors.Is(err, pgx.ErrNoRows) {
		return Rule{}, nil, ErrNotFound
	}
	if err != nil {
		return Rule{}, nil, fmt.Errorf("guardar regla: %w", err)
	}
	// Con otras condiciones, las cuentas y los cruces avisados ya no valen; el
	// último aviso se conserva para el límite de la regla.
	before, _ := json.Marshal(old.Conditions)
	after, _ := json.Marshal(out.Conditions)
	if !bytes.Equal(before, after) {
		for _, q := range []string{
			`DELETE FROM alert_rule_minutes WHERE rule_id = $1`,
			`DELETE FROM alert_rule_users WHERE rule_id = $1`,
			`UPDATE alert_rule_issues SET frequency_alerted = false, users_alerted = false WHERE rule_id = $1`,
		} {
			if _, err := tx.Exec(ctx, q, r.ID); err != nil {
				return Rule{}, nil, fmt.Errorf("reiniciar las cuentas de la regla: %w", err)
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return Rule{}, nil, fmt.Errorf("guardar regla: %w", err)
	}
	return out, generated, nil
}

// DeleteRule borra la regla y los avisos suyos que quedaran por enviar.
func (s *Store) DeleteRule(ctx context.Context, projectID, id int64) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM alert_rules WHERE project_id = $1 AND id = $2`, projectID, id)
	if err != nil {
		return fmt.Errorf("borrar regla: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
