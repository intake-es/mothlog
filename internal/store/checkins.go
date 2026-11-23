package store

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/intake-es/intake-errors/internal/cron"
	"github.com/intake-es/intake-errors/internal/db"
)

// MonitorConfig es el monitor_config de un check-in: crea el monitor o lo
// sustituye. Lo que no trae vuelve a su valor por defecto.
type MonitorConfig struct {
	Schedule      cron.Schedule
	CheckinMargin *int
	MaxRuntime    *int
	Timezone      string
}

// CheckInInput es un check-in de un SDK o de sentry-cli.
type CheckInInput struct {
	// CheckInID son 32 caracteres hex; vacío, uno nuevo.
	CheckInID   string
	Slug        string
	Status      string
	Duration    *float64
	Environment string
	Release     string
	Config      *MonitorConfig
}

// MonitorChange es un cambio de estado de un monitor que puede avisar.
type MonitorChange struct {
	Monitor  Monitor
	CheckIn  CheckIn
	Kind     string
	Previous string
}

// MonitorNotifier encola los avisos de un cambio de estado en la transacción que lo guarda.
type MonitorNotifier interface {
	MonitorChanged(ctx context.Context, tx db.Tx, ch MonitorChange) error
}

// Qué pasó con un check-in.
const (
	CheckInCreated = "created"
	CheckInUpdated = "updated"
	CheckInIgnored = "ignored"
)

type CheckInResult struct {
	Outcome string
	// Reason dice por qué se ignoró.
	Reason  string
	Monitor Monitor
	CheckIn CheckIn
	// Created es que el check-in creó el monitor.
	Created bool
}

// MaxAutoMonitors es cuántos monitores puede tener un proyecto para que un
// check-in cree otro; por encima, solo por la API.
const MaxAutoMonitors = 500

// expectedSlack es lo que puede adelantarse un check-in a su hora y seguir
// contando como la ejecución que tocaba.
const expectedSlack = time.Minute

func failing(status string) bool {
	return status == CheckInError || status == CheckInMissed || status == CheckInTimeout
}

// changeKind es el aviso que da pasar de prev a next, o "" si no da ninguno:
// los fallos avisan al empezar o al cambiar de tipo, y ok tras un fallo es
// que se ha recuperado.
func changeKind(prev, next string) string {
	if prev == next {
		return ""
	}
	switch next {
	case CheckInError:
		return KindCronFailed
	case CheckInMissed:
		return KindCronMissed
	case CheckInTimeout:
		return KindCronTimeout
	case CheckInOK:
		if failing(prev) {
			return KindCronRecovered
		}
	}
	return ""
}

func (c *MonitorConfig) input(slug string) (MonitorInput, error) {
	margin, runtime := DefaultCheckinMargin, DefaultMaxRuntime
	if c.CheckinMargin != nil && *c.CheckinMargin > 0 {
		margin = *c.CheckinMargin
	}
	if c.MaxRuntime != nil && *c.MaxRuntime > 0 {
		runtime = *c.MaxRuntime
	}
	tz := c.Timezone
	if tz == "" {
		tz = "UTC"
	}
	sched := c.Schedule
	in := MonitorInput{Slug: &slug, Schedule: &sched, Timezone: &tz, CheckinMargin: &margin, MaxRuntime: &runtime}
	var m Monitor
	return in, in.apply(&m)
}

// ProcessCheckIn guarda el check-in, crea o actualiza el monitor con su
// monitor_config y, si cambia de estado, se lo dice a notify en la misma
// transacción. now es la hora de llegada.
func (s *Store) ProcessCheckIn(ctx context.Context, projectID int64, in CheckInInput, now time.Time,
	notify MonitorNotifier) (CheckInResult, error) {
	switch in.Status {
	case CheckInInProgress, CheckInOK, CheckInError:
	default:
		return CheckInResult{Outcome: CheckInIgnored, Reason: "estado desconocido: " + in.Status}, nil
	}
	in.Slug = Slugify(in.Slug)
	if in.Slug == "" {
		return CheckInResult{Outcome: CheckInIgnored, Reason: "sin monitor_slug"}, nil
	}
	if in.CheckInID == "" {
		in.CheckInID = newCheckInID()
	}
	now = now.UTC()
	var res CheckInResult
	err := db.BeginFunc(ctx, s.pool, func(tx db.Tx) error {
		res = CheckInResult{}
		var cfg *MonitorInput
		if in.Config != nil {
			c, err := in.Config.input(in.Slug)
			if err != nil {
				res.Reason = "monitor_config no válido: " + err.Error()
			} else {
				cfg = &c
			}
		}
		m, err := lockMonitor(ctx, tx, projectID, in.Slug)
		if errors.Is(err, ErrNotFound) {
			if cfg == nil {
				res.Outcome = CheckInIgnored
				if res.Reason == "" {
					res.Reason = "el monitor no existe y el check-in no trae monitor_config"
				}
				return nil
			}
			var n int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM monitors WHERE project_id = $1`, projectID).Scan(&n); err != nil {
				return fmt.Errorf("contar monitores: %w", err)
			}
			if n >= MaxAutoMonitors {
				res.Outcome = CheckInIgnored
				res.Reason = fmt.Sprintf("el proyecto ya tiene %d monitores; los demás se crean por la API", n)
				return nil
			}
			// Sin entorno: vale cualquiera. Se fija a mano por la API.
			if m, err = createMonitorTx(ctx, tx, projectID, *cfg); err != nil {
				return err
			}
			res.Created = true
		} else if err != nil {
			return err
		} else if cfg != nil {
			if m, err = configureMonitor(ctx, tx, m, *cfg); err != nil {
				return err
			}
		}
		res.Monitor = m
		if m.Environment != "" && in.Environment != m.Environment {
			res.Outcome, res.Reason = CheckInIgnored, "el monitor vigila el entorno "+m.Environment
			return nil
		}
		return s.saveCheckIn(ctx, tx, &res, in, now, notify)
	})
	return res, err
}

func createMonitorTx(ctx context.Context, tx db.Tx, projectID int64, in MonitorInput) (Monitor, error) {
	m := Monitor{}
	if err := in.apply(&m); err != nil {
		return Monitor{}, err
	}
	sched, every, unit := scheduleArgs(m.Schedule)
	// Si otro check-in lo crea a la vez, se espera a él y se usa el suyo.
	if _, err := tx.Exec(ctx, `
		INSERT INTO monitors (project_id, slug, name, schedule_type, schedule, interval_value, interval_unit,
			timezone, checkin_margin, max_runtime, environment)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		ON CONFLICT (project_id, slug) DO NOTHING`, projectID, m.Slug, m.Name, m.Schedule.Type, sched, every, unit,
		m.Timezone, m.CheckinMargin, m.MaxRuntime, m.Environment); err != nil {
		return Monitor{}, fmt.Errorf("crear monitor: %w", err)
	}
	return lockMonitor(ctx, tx, projectID, m.Slug)
}

// configureMonitor cambia el horario, los márgenes y la zona del monitor si
// el monitor_config dice otra cosa.
func configureMonitor(ctx context.Context, tx db.Tx, m Monitor, in MonitorInput) (Monitor, error) {
	old := m
	in.Slug = nil
	if err := in.apply(&m); err != nil {
		return old, nil
	}
	if m.Schedule == old.Schedule && m.Timezone == old.Timezone &&
		m.CheckinMargin == old.CheckinMargin && m.MaxRuntime == old.MaxRuntime {
		return m, nil
	}
	if (m.Schedule != old.Schedule || m.Timezone != old.Timezone) && m.LastCheckinAt != nil {
		next := m.Schedule.Next(*m.LastCheckinAt, m.Location())
		m.NextCheckinAt = &next
	}
	sched, every, unit := scheduleArgs(m.Schedule)
	_, err := tx.Exec(ctx, `
		UPDATE monitors SET schedule_type = $2, schedule = $3, interval_value = $4, interval_unit = $5,
			timezone = $6, checkin_margin = $7, max_runtime = $8, next_checkin_at = $9, updated_at = now()
		WHERE id = $1`, m.ID, m.Schedule.Type, sched, every, unit, m.Timezone, m.CheckinMargin, m.MaxRuntime,
		m.NextCheckinAt)
	if err != nil {
		return m, fmt.Errorf("actualizar monitor: %w", err)
	}
	return m, nil
}

func durationMs(d *float64) *int64 {
	if d == nil || *d < 0 || math.IsNaN(*d) || math.IsInf(*d, 0) {
		return nil
	}
	ms := int64(math.Round(*d * 1000))
	return &ms
}

func (s *Store) saveCheckIn(ctx context.Context, tx db.Tx, res *CheckInResult, in CheckInInput, now time.Time,
	notify MonitorNotifier) error {
	m := &res.Monitor
	dur := durationMs(in.Duration)
	old, err := scanCheckIn(tx.QueryRow(ctx, `SELECT `+checkInColumns+` FROM monitor_checkins
		WHERE monitor_id = $1 AND check_in_id = $2`+tx.Dialect().ForUpdate(), m.ID, in.CheckInID))
	switch {
	case errors.Is(err, db.ErrNoRows):
	case err != nil:
		return fmt.Errorf("leer check-in: %w", err)
	default:
		return s.closeCheckIn(ctx, tx, res, old, in, dur, now, notify)
	}

	c := CheckIn{MonitorID: m.ID, CheckInID: in.CheckInID, Status: in.Status, DurationMs: dur,
		Environment: in.Environment, Release: in.Release, StartedAt: now, ReceivedAt: now}
	// Una ejecución a mano, antes de su hora, no es la que tocaba.
	if next := m.NextCheckinAt; next != nil && !next.After(now.Add(expectedSlack)) {
		c.ExpectedAt = next
	}
	if in.Status == CheckInInProgress {
		t := now.Add(time.Duration(m.MaxRuntime) * time.Minute)
		c.TimeoutAt = &t
	} else {
		c.FinishedAt = &now
		if dur != nil {
			c.StartedAt = now.Add(-time.Duration(*dur) * time.Millisecond)
		}
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO monitor_checkins (monitor_id, check_in_id, status, duration_ms, environment, release,
			expected_at, started_at, finished_at, timeout_at, received_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		RETURNING id, `+checkInIDColumn, c.MonitorID, c.CheckInID, c.Status, c.DurationMs, c.Environment, c.Release,
		c.ExpectedAt, c.StartedAt, c.FinishedAt, c.TimeoutAt, c.ReceivedAt).Scan(&c.ID, &c.CheckInID); err != nil {
		return fmt.Errorf("guardar check-in: %w", err)
	}
	res.Outcome, res.CheckIn = CheckInCreated, c

	next := m.Schedule.Next(now, m.Location())
	m.LastCheckinAt, m.NextCheckinAt = &now, &next
	if next.IsZero() {
		m.NextCheckinAt = nil
	}
	if _, err := tx.Exec(ctx, `UPDATE monitors SET last_checkin_at = $2, next_checkin_at = $3 WHERE id = $1`,
		m.ID, m.LastCheckinAt, m.NextCheckinAt); err != nil {
		return fmt.Errorf("actualizar monitor: %w", err)
	}
	if c.Status == CheckInInProgress {
		return nil
	}
	return setMonitorStatus(ctx, tx, m, c, notify)
}

// closeCheckIn cierra un check-in in_progress con el ok o el error que lo
// acaba. Uno que ya se dio por timeout guarda la duración y sigue en timeout;
// el resto de repeticiones se ignoran.
func (s *Store) closeCheckIn(ctx context.Context, tx db.Tx, res *CheckInResult, c CheckIn, in CheckInInput,
	dur *int64, now time.Time, notify MonitorNotifier) error {
	res.CheckIn = c
	if in.Status == CheckInInProgress || (c.Status != CheckInInProgress && c.Status != CheckInTimeout) ||
		(c.Status == CheckInTimeout && c.FinishedAt != nil) {
		res.Outcome, res.Reason = CheckInIgnored, "el check-in ya está "+c.Status
		return nil
	}
	if dur == nil {
		d := now.Sub(c.StartedAt).Milliseconds()
		dur = &d
	}
	status := in.Status
	if c.Status == CheckInTimeout {
		status = CheckInTimeout
	}
	if _, err := tx.Exec(ctx, `
		UPDATE monitor_checkins SET status = $2, duration_ms = $3, finished_at = $4, timeout_at = NULL,
			environment = COALESCE(NULLIF($5, ''), environment), release = COALESCE(NULLIF($6, ''), release)
		WHERE id = $1`, c.ID, status, dur, now, in.Environment, in.Release); err != nil {
		return fmt.Errorf("cerrar check-in: %w", err)
	}
	c.Status, c.DurationMs, c.FinishedAt, c.TimeoutAt = status, dur, &now, nil
	res.Outcome, res.CheckIn = CheckInUpdated, c
	if status == CheckInTimeout {
		return nil
	}
	return setMonitorStatus(ctx, tx, &res.Monitor, c, notify)
}

func setMonitorStatus(ctx context.Context, tx db.Tx, m *Monitor, c CheckIn, notify MonitorNotifier) error {
	prev := m.Status
	if prev == c.Status {
		return nil
	}
	if _, err := tx.Exec(ctx, `UPDATE monitors SET status = $2 WHERE id = $1`, m.ID, c.Status); err != nil {
		return fmt.Errorf("actualizar monitor: %w", err)
	}
	m.Status = c.Status
	kind := changeKind(prev, c.Status)
	if kind == "" || m.Muted || notify == nil {
		return nil
	}
	return notify.MonitorChanged(ctx, tx, MonitorChange{Monitor: *m, CheckIn: c, Kind: kind, Previous: prev})
}

// SweepMonitors da por timeout los check-ins in_progress que se han pasado de
// su tiempo y por missed los monitores cuyo check-in no ha llegado dentro del
// margen. Coge cada uno con FOR UPDATE SKIP LOCKED en su propia transacción:
// con varias réplicas a la vez, cada cosa la marca una sola.
func (s *Store) SweepMonitors(ctx context.Context, now time.Time, notify MonitorNotifier) (missed, timeouts int, err error) {
	now = now.UTC()
	for ctx.Err() == nil {
		ok, err := s.sweepTimeout(ctx, now, notify)
		if err != nil {
			return missed, timeouts, err
		}
		if !ok {
			break
		}
		timeouts++
	}
	for ctx.Err() == nil {
		ok, err := s.sweepMissed(ctx, now, notify)
		if err != nil {
			return missed, timeouts, err
		}
		if !ok {
			break
		}
		missed++
	}
	return missed, timeouts, ctx.Err()
}

func (s *Store) sweepTimeout(ctx context.Context, now time.Time, notify MonitorNotifier) (bool, error) {
	found := false
	err := db.BeginFunc(ctx, s.pool, func(tx db.Tx) error {
		var checkinID, monitorID int64
		err := tx.QueryRow(ctx, `
			SELECT c.id, c.monitor_id FROM monitor_checkins c JOIN monitors m ON m.id = c.monitor_id
			WHERE c.status = 'in_progress' AND c.timeout_at < $1
			ORDER BY c.timeout_at, c.id
			LIMIT 1`+tx.Dialect().ForUpdateSkipLocked("c", "m"), now).Scan(&checkinID, &monitorID)
		if errors.Is(err, db.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("buscar check-ins pasados de tiempo: %w", err)
		}
		found = true
		m, err := scanMonitor(tx.QueryRow(ctx, `SELECT `+monitorColumns+monitorFrom+` WHERE m.id = $1`, monitorID))
		if err != nil {
			return fmt.Errorf("leer monitor: %w", err)
		}
		c, err := scanCheckIn(tx.QueryRow(ctx, `UPDATE monitor_checkins SET status = 'timeout'
			WHERE id = $1 RETURNING `+checkInColumns, checkinID))
		if err != nil {
			return fmt.Errorf("marcar timeout: %w", err)
		}
		return setMonitorStatus(ctx, tx, &m, c, notify)
	})
	return found, err
}

func (s *Store) sweepMissed(ctx context.Context, now time.Time, notify MonitorNotifier) (bool, error) {
	found := false
	err := db.BeginFunc(ctx, s.pool, func(tx db.Tx) error {
		m, err := scanMonitor(tx.QueryRow(ctx, `SELECT `+monitorColumns+monitorFrom+`
			WHERE m.next_checkin_at <= $1 AND `+tx.Dialect().AddMinutes("m.next_checkin_at", "m.checkin_margin")+` <= $1
			ORDER BY m.next_checkin_at, m.id
			LIMIT 1`+tx.Dialect().ForUpdateSkipLocked("m"), now))
		if errors.Is(err, db.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("buscar monitores sin check-in: %w", err)
		}
		found = true
		expected := *m.NextCheckinAt
		c := CheckIn{MonitorID: m.ID, CheckInID: newCheckInID(), Status: CheckInMissed,
			ExpectedAt: &expected, StartedAt: expected, ReceivedAt: now}
		// Sin entorno fijo, el missed es del entorno del último check-in que llegó.
		if err := tx.QueryRow(ctx, `
			INSERT INTO monitor_checkins (monitor_id, check_in_id, status, environment, expected_at, started_at, received_at)
			VALUES ($1, $2, 'missed', COALESCE(NULLIF($3, ''), (SELECT environment FROM monitor_checkins
				WHERE monitor_id = $1 AND status <> 'missed' ORDER BY received_at DESC, id DESC LIMIT 1), ''), $4, $4, $5)
			RETURNING id, `+checkInIDColumn+`, environment`,
			m.ID, c.CheckInID, m.Environment, expected, now).Scan(&c.ID, &c.CheckInID, &c.Environment); err != nil {
			return fmt.Errorf("guardar missed: %w", err)
		}
		// Si se han perdido varias, se apunta una y se sigue desde la que aún puede llegar.
		loc := m.Location()
		margin := time.Duration(m.CheckinMargin) * time.Minute
		next := m.Schedule.Next(expected, loc)
		if !next.IsZero() && !next.Add(margin).After(now) {
			next = m.Schedule.Next(now.Add(-margin), loc)
		}
		var nextPtr *time.Time
		if !next.IsZero() {
			nextPtr = &next
		}
		if _, err := tx.Exec(ctx, `UPDATE monitors SET next_checkin_at = $2 WHERE id = $1`, m.ID, nextPtr); err != nil {
			return fmt.Errorf("actualizar monitor: %w", err)
		}
		m.NextCheckinAt = nextPtr
		return setMonitorStatus(ctx, tx, &m, c, notify)
	})
	return found, err
}
