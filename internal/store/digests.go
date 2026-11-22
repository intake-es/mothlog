package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Cada cuánto va el resumen de un proyecto.
const (
	DigestDaily  = "daily"
	DigestWeekly = "weekly"
)

// KindDigest es el tipo de los resúmenes en la tabla de salida.
const KindDigest = "digest"

// DigestDestinationTypes son las acciones que valen como destino de un resumen.
var DigestDestinationTypes = []string{ActionEmail, ActionSlack, ActionWebhook}

// DigestConfig es el resumen periódico de un proyecto: cada cuánto, a qué
// hora en qué zona horaria, en qué idioma y a dónde va.
type DigestConfig struct {
	ProjectID int64  `json:"project_id"`
	Enabled   bool   `json:"enabled"`
	Frequency string `json:"frequency"`
	// Weekday es el día del semanal: 0 es domingo y 6 sábado.
	Weekday  int    `json:"weekday"`
	Hour     int    `json:"hour"`
	Timezone string `json:"timezone"`
	Language string `json:"language"`
	// SendEmpty: un periodo sin nada manda un resumen corto que lo dice; si no, ninguno.
	SendEmpty    bool         `json:"send_empty"`
	Destinations []RuleAction `json:"destinations"`
	// LastPeriodEnd es el final del último periodo encolado.
	LastPeriodEnd *time.Time `json:"last_period_end"`
	LastSentAt    *time.Time `json:"last_sent_at"`
	// NextSendAt es cuándo toca el siguiente; nil si está apagado.
	NextSendAt *time.Time `json:"next_send_at"`
	UpdatedAt  *time.Time `json:"updated_at"`
}

// DefaultDigest es el resumen de un proyecto que no lo ha configurado: apagado, diario a las 9 UTC.
func DefaultDigest(projectID int64) DigestConfig {
	return DigestConfig{ProjectID: projectID, Frequency: DigestDaily, Weekday: 1, Hour: 9, Timezone: "UTC",
		Language: "es", SendEmpty: true, Destinations: []RuleAction{}}
}

// Location es la zona horaria del resumen; si no se puede cargar, UTC.
func (c DigestConfig) Location() *time.Location {
	if loc, err := time.LoadLocation(c.Timezone); err == nil {
		return loc
	}
	return time.UTC
}

// sendAt es la hora de envío del día y, d días después.
func (c DigestConfig) sendAt(day time.Time, d int) time.Time {
	return time.Date(day.Year(), day.Month(), day.Day()+d, c.Hour, 0, 0, 0, day.Location())
}

// PeriodEnd es la última hora de envío que no es posterior a now: el final
// del último periodo cerrado. Las horas son las del reloj de la zona, así
// que un periodo diario dura 23 o 25 horas en los cambios de hora.
func (c DigestConfig) PeriodEnd(now time.Time) time.Time {
	local := now.In(c.Location())
	back := 0
	if c.Frequency == DigestWeekly {
		back = (int(local.Weekday()) - c.Weekday + 7) % 7
	}
	end := c.sendAt(local, -back)
	if end.After(now) {
		end = c.sendAt(local, -back-c.days())
	}
	return end
}

// PeriodStart es el principio del periodo que acaba en end.
func (c DigestConfig) PeriodStart(end time.Time) time.Time {
	return c.sendAt(end.In(c.Location()), -c.days())
}

// NextSend es la primera hora de envío posterior a now.
func (c DigestConfig) NextSend(now time.Time) time.Time {
	return c.sendAt(c.PeriodEnd(now).In(c.Location()), c.days())
}

// Length es lo que dura un periodo, para el que se pide a mano: 24 horas o 7 días.
func (c DigestConfig) Length() time.Duration {
	return time.Duration(c.days()) * 24 * time.Hour
}

func (c DigestConfig) days() int {
	if c.Frequency == DigestWeekly {
		return 7
	}
	return 1
}

// sameSchedule dice si dos configuraciones mandan a las mismas horas.
func (c DigestConfig) sameSchedule(o DigestConfig) bool {
	return c.Enabled == o.Enabled && c.Frequency == o.Frequency && c.Hour == o.Hour && c.Timezone == o.Timezone &&
		(c.Frequency != DigestWeekly || c.Weekday == o.Weekday)
}

// Masked devuelve la configuración con los secretos tapados, salvo los de los destinos de reveal.
func (c DigestConfig) Masked(reveal ...string) DigestConfig {
	c.Destinations = Rule{Actions: c.Destinations}.Masked(reveal...).Actions
	return c
}

// Normalize limpia la configuración y comprueba que es válida. Da un id a los
// destinos que no lo tienen y un secreto a los webhooks que no lo traen, y
// devuelve los ids de los destinos con el secreto recién generado.
func (c *DigestConfig) Normalize() ([]string, error) {
	c.Timezone = strings.TrimSpace(c.Timezone)
	if c.Timezone == "" {
		c.Timezone = "UTC"
	}
	switch {
	case c.Frequency != DigestDaily && c.Frequency != DigestWeekly:
		return nil, fmt.Errorf("%w: frequency es daily o weekly", ErrInvalid)
	case c.Weekday < 0 || c.Weekday > 6:
		return nil, fmt.Errorf("%w: weekday va de 0 (domingo) a 6 (sábado)", ErrInvalid)
	case c.Hour < 0 || c.Hour > 23:
		return nil, fmt.Errorf("%w: hour va de 0 a 23", ErrInvalid)
	case c.Language != "es" && c.Language != "en":
		return nil, fmt.Errorf("%w: language es es o en", ErrInvalid)
	case len(c.Destinations) > maxRuleActions:
		return nil, fmt.Errorf("%w: el resumen admite hasta %d destinos", ErrInvalid, maxRuleActions)
	}
	if _, err := time.LoadLocation(c.Timezone); err != nil || c.Timezone == "Local" {
		return nil, fmt.Errorf("%w: la zona horaria %q no existe", ErrInvalid, c.Timezone)
	}
	if c.Enabled && len(c.Destinations) == 0 {
		return nil, fmt.Errorf("%w: el resumen necesita al menos un destino", ErrInvalid)
	}
	var generated []string
	seen := map[string]bool{}
	for i := range c.Destinations {
		a := &c.Destinations[i]
		if !slices.Contains(DigestDestinationTypes, a.Type) {
			return nil, fmt.Errorf("%w: el resumen va por correo, Slack o webhook, no por %q", ErrInvalid, a.Type)
		}
		a.ID = strings.TrimSpace(a.ID)
		if a.ID == "" {
			a.ID = hexID(4)
		}
		if seen[a.ID] || len(a.ID) > 64 {
			return nil, fmt.Errorf("%w: id de destino repetido o demasiado largo: %q", ErrInvalid, a.ID)
		}
		seen[a.ID] = true
		gen, err := a.normalize()
		if err != nil {
			return nil, fmt.Errorf("%w (destino %d, %s)", err, i+1, a.Type)
		}
		if gen {
			generated = append(generated, a.ID)
		}
	}
	if c.Destinations == nil {
		c.Destinations = []RuleAction{}
	}
	return generated, nil
}

const digestColumns = `project_id, enabled, frequency, weekday, hour, timezone, language, send_empty, destinations,
	last_period_end, last_sent_at, updated_at`

func scanDigest(r pgx.Row, now time.Time) (DigestConfig, error) {
	var c DigestConfig
	err := r.Scan(&c.ProjectID, &c.Enabled, &c.Frequency, &c.Weekday, &c.Hour, &c.Timezone, &c.Language,
		&c.SendEmpty, &c.Destinations, &c.LastPeriodEnd, &c.LastSentAt, &c.UpdatedAt)
	if err == nil && c.Enabled {
		next := c.NextSend(now)
		c.NextSendAt = &next
	}
	return c, err
}

// Digest da la configuración del resumen del proyecto, con los secretos enteros.
func (s *Store) Digest(ctx context.Context, projectID int64, now time.Time) (DigestConfig, error) {
	c, err := scanDigest(s.pool.QueryRow(ctx, `SELECT `+digestColumns+` FROM project_digests WHERE project_id = $1`, projectID), now)
	if errors.Is(err, pgx.ErrNoRows) {
		if _, err := s.ProjectByID(ctx, projectID); err != nil {
			return DigestConfig{}, err
		}
		return DefaultDigest(projectID), nil
	}
	if err != nil {
		return DigestConfig{}, fmt.Errorf("leer el resumen: %w", err)
	}
	return c, nil
}

// SetDigest guarda la configuración del resumen. Los secretos que llegan
// vacíos o tapados se conservan. Al encenderlo o cambiar la hora, el periodo
// que acaba de cerrarse se da por mandado: el primero sale a la hora siguiente.
func (s *Store) SetDigest(ctx context.Context, projectID int64, c DigestConfig, now time.Time) (DigestConfig, []string, error) {
	old, err := s.Digest(ctx, projectID, now)
	if err != nil {
		return DigestConfig{}, nil, err
	}
	r := Rule{Actions: c.Destinations}
	r.keepSecrets(Rule{Actions: old.Destinations})
	c.Destinations = r.Actions
	generated, err := c.Normalize()
	if err != nil {
		return DigestConfig{}, nil, err
	}
	last := old.LastPeriodEnd
	if last == nil || !c.sameSchedule(old) {
		end := c.PeriodEnd(now)
		last = &end
	}
	out, err := scanDigest(s.pool.QueryRow(ctx, `
		INSERT INTO project_digests (project_id, enabled, frequency, weekday, hour, timezone, language, send_empty,
			destinations, last_period_end, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, now())
		ON CONFLICT (project_id) DO UPDATE SET enabled = EXCLUDED.enabled, frequency = EXCLUDED.frequency,
			weekday = EXCLUDED.weekday, hour = EXCLUDED.hour, timezone = EXCLUDED.timezone, language = EXCLUDED.language,
			send_empty = EXCLUDED.send_empty, destinations = EXCLUDED.destinations,
			last_period_end = EXCLUDED.last_period_end, updated_at = now()
		RETURNING `+digestColumns,
		projectID, c.Enabled, c.Frequency, c.Weekday, c.Hour, c.Timezone, c.Language, c.SendEmpty, c.Destinations, last), now)
	if err != nil {
		return DigestConfig{}, nil, fmt.Errorf("guardar el resumen: %w", err)
	}
	return out, generated, nil
}

// DigestsDue son los resúmenes encendidos con un periodo cerrado sin encolar.
func (s *Store) DigestsDue(ctx context.Context, now time.Time) ([]DigestConfig, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+digestColumns+` FROM project_digests WHERE enabled ORDER BY project_id`)
	if err != nil {
		return nil, fmt.Errorf("leer los resúmenes: %w", err)
	}
	all, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (DigestConfig, error) { return scanDigest(r, now) })
	if err != nil {
		return nil, fmt.Errorf("leer los resúmenes: %w", err)
	}
	return slices.DeleteFunc(all, func(c DigestConfig) bool {
		return c.LastPeriodEnd != nil && !c.PeriodEnd(now).After(*c.LastPeriodEnd)
	}), nil
}

// DigestDetail es lo que lleva en la tabla de salida un resumen: dónde empieza el periodo.
type DigestDetail struct {
	PeriodStart time.Time `json:"period_start"`
}

// EnqueueDigest encola, si nadie lo ha hecho ya, el resumen del periodo
// [start, end): una fila por destino o ninguna si send es false. Lo apunta en
// la misma transacción, con la fila del proyecto bloqueada, para que dos
// réplicas o un reinicio no lo encolen dos veces. Devuelve si lo ha encolado.
func (s *Store) EnqueueDigest(ctx context.Context, projectID int64, start, end time.Time, send bool, now time.Time) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var enabled bool
	var last *time.Time
	var dests []RuleAction
	err = tx.QueryRow(ctx, `SELECT enabled, last_period_end, destinations FROM project_digests WHERE project_id = $1
		FOR UPDATE SKIP LOCKED`, projectID).Scan(&enabled, &last, &dests)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("encolar el resumen: %w", err)
	}
	if !enabled || (last != nil && !end.After(*last)) {
		return false, nil
	}
	if send {
		detail, _ := json.Marshal(DigestDetail{PeriodStart: start.UTC()})
		for _, d := range dests {
			if _, err := tx.Exec(ctx, `
				INSERT INTO alert_outbox (digest_project_id, channel, kind, event_at, next_attempt_at, created_at, action_id, detail)
				VALUES ($1, $2, $3, $4, $5, $5, $6, $7)
				ON CONFLICT (digest_project_id, action_id, event_at) WHERE digest_project_id IS NOT NULL DO NOTHING`,
				projectID, d.Type, KindDigest, end.UTC(), now.UTC(), d.ID, detail); err != nil {
				return false, fmt.Errorf("encolar el resumen: %w", err)
			}
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE project_digests SET last_period_end = $2 WHERE project_id = $1`, projectID, end.UTC()); err != nil {
		return false, fmt.Errorf("encolar el resumen: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("encolar el resumen: %w", err)
	}
	return true, nil
}
