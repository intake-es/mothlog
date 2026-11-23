package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/intake-es/intake-errors/internal/cron"
	"github.com/intake-es/intake-errors/internal/db"
)

// Estados de un monitor y de sus check-ins.
const (
	MonitorPending    = "pending"
	CheckInInProgress = "in_progress"
	CheckInOK         = "ok"
	CheckInError      = "error"
	CheckInMissed     = "missed"
	CheckInTimeout    = "timeout"
)

// Tipos de aviso de un cron.
const (
	KindCronFailed    = "cron_failed"
	KindCronMissed    = "cron_missed"
	KindCronTimeout   = "cron_timeout"
	KindCronRecovered = "cron_recovered"
)

const (
	DefaultCheckinMargin = 1
	DefaultMaxRuntime    = 30
	// MaxMonitorMinutes es lo más que admiten checkin_margin y max_runtime: 28 días.
	MaxMonitorMinutes = 28 * 24 * 60
	maxSlug           = 64
	maxMonitorName    = 200
	// RecentCheckIns son los check-ins que lleva cada monitor de la lista.
	RecentCheckIns = 30
)

var reSlug = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// Monitor es un cron que avisa con check-ins.
type Monitor struct {
	ID        int64         `json:"id"`
	ProjectID int64         `json:"project_id"`
	Project   string        `json:"project"`
	Slug      string        `json:"slug"`
	Name      string        `json:"name"`
	Schedule  cron.Schedule `json:"schedule"`
	Timezone  string        `json:"timezone"`
	// CheckinMargin y MaxRuntime van en minutos.
	CheckinMargin int        `json:"checkin_margin"`
	MaxRuntime    int        `json:"max_runtime"`
	Environment   string     `json:"environment"`
	Muted         bool       `json:"muted"`
	Status        string     `json:"status"`
	LastCheckinAt *time.Time `json:"last_checkin_at"`
	NextCheckinAt *time.Time `json:"next_checkin_at"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
	// Recent son los últimos check-ins, del más nuevo al más viejo; solo en la lista.
	Recent []CheckInMark `json:"recent,omitempty"`
}

// CheckInMark es un check-in de la tira de la lista.
type CheckInMark struct {
	Status     string    `json:"status"`
	StartedAt  time.Time `json:"started_at"`
	DurationMs *int64    `json:"duration_ms"`
}

// CheckIn es una ejecución de un monitor.
type CheckIn struct {
	ID          int64      `json:"id"`
	MonitorID   int64      `json:"monitor_id"`
	CheckInID   string     `json:"check_in_id"`
	Status      string     `json:"status"`
	DurationMs  *int64     `json:"duration_ms"`
	Environment string     `json:"environment"`
	Release     string     `json:"release"`
	ExpectedAt  *time.Time `json:"expected_at"`
	StartedAt   time.Time  `json:"started_at"`
	FinishedAt  *time.Time `json:"finished_at"`
	TimeoutAt   *time.Time `json:"timeout_at,omitempty"`
	ReceivedAt  time.Time  `json:"received_at"`
}

// Location es la zona horaria del monitor.
func (m Monitor) Location() *time.Location {
	loc, err := cron.LoadLocation(m.Timezone)
	if err != nil {
		return time.UTC
	}
	return loc
}

// MonitorInput crea o cambia un monitor; los campos nil no cambian.
type MonitorInput struct {
	Slug          *string        `json:"slug"`
	Name          *string        `json:"name"`
	Schedule      *cron.Schedule `json:"schedule"`
	Timezone      *string        `json:"timezone"`
	CheckinMargin *int           `json:"checkin_margin"`
	MaxRuntime    *int           `json:"max_runtime"`
	Environment   *string        `json:"environment"`
	Muted         *bool          `json:"muted"`
}

// Slugify deja un slug como los de los SDK: minúsculas, números, - y _.
func Slugify(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_':
			b.WriteRune(r)
			dash = false
		case !dash && b.Len() > 0:
			b.WriteByte('-')
			dash = true
		}
	}
	out := strings.TrimRight(b.String(), "-")
	if len(out) > maxSlug {
		out = strings.TrimRight(out[:maxSlug], "-")
	}
	return out
}

// apply pone en m los campos de in y comprueba que el resultado es válido.
func (in MonitorInput) apply(m *Monitor) error {
	if in.Slug != nil {
		m.Slug = strings.TrimSpace(*in.Slug)
	}
	if in.Name != nil {
		m.Name = strings.TrimSpace(*in.Name)
	}
	if in.Schedule != nil {
		m.Schedule = *in.Schedule
	}
	if in.Timezone != nil {
		m.Timezone = strings.TrimSpace(*in.Timezone)
	}
	if in.CheckinMargin != nil {
		m.CheckinMargin = *in.CheckinMargin
	}
	if in.MaxRuntime != nil {
		m.MaxRuntime = *in.MaxRuntime
	}
	if in.Environment != nil {
		m.Environment = strings.TrimSpace(*in.Environment)
	}
	if in.Muted != nil {
		m.Muted = *in.Muted
	}
	switch {
	case m.Slug == "" || len(m.Slug) > maxSlug || !reSlug.MatchString(m.Slug):
		return fmt.Errorf("%w: el slug va en minúsculas, números, - y _, hasta %d caracteres", ErrInvalid, maxSlug)
	case len([]rune(m.Name)) > maxMonitorName:
		return fmt.Errorf("%w: el nombre admite hasta %d caracteres", ErrInvalid, maxMonitorName)
	case m.CheckinMargin < 1 || m.CheckinMargin > MaxMonitorMinutes:
		return fmt.Errorf("%w: checkin_margin va de 1 a %d minutos", ErrInvalid, MaxMonitorMinutes)
	case m.MaxRuntime < 1 || m.MaxRuntime > MaxMonitorMinutes:
		return fmt.Errorf("%w: max_runtime va de 1 a %d minutos", ErrInvalid, MaxMonitorMinutes)
	case len(m.Environment) > maxRuleValue:
		return fmt.Errorf("%w: el entorno admite hasta %d caracteres", ErrInvalid, maxRuleValue)
	}
	if m.Name == "" {
		m.Name = m.Slug
	}
	if m.Schedule.Type == "" {
		return fmt.Errorf("%w: falta schedule", ErrInvalid)
	}
	if err := m.Schedule.Normalize(); err != nil {
		return fmt.Errorf("%w: schedule: %v", ErrInvalid, err)
	}
	if m.Timezone == "" {
		m.Timezone = "UTC"
	}
	if _, err := cron.LoadLocation(m.Timezone); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	return nil
}

const monitorColumns = `m.id, m.project_id, p.name, m.slug, m.name, m.schedule_type, m.schedule,
	COALESCE(m.interval_value, 0), COALESCE(m.interval_unit, ''), m.timezone, m.checkin_margin, m.max_runtime,
	m.environment, m.muted, m.status, m.last_checkin_at, m.next_checkin_at, m.created_at, m.updated_at`

const monitorFrom = ` FROM monitors m JOIN projects p ON p.id = m.project_id`

func scanMonitor(r db.Row) (Monitor, error) {
	var m Monitor
	err := r.Scan(&m.ID, &m.ProjectID, &m.Project, &m.Slug, &m.Name, &m.Schedule.Type, &m.Schedule.Crontab,
		&m.Schedule.Every, &m.Schedule.Unit, &m.Timezone, &m.CheckinMargin, &m.MaxRuntime,
		&m.Environment, &m.Muted, &m.Status, &m.LastCheckinAt, &m.NextCheckinAt, &m.CreatedAt, &m.UpdatedAt)
	return m, err
}

// scheduleArgs son schedule, interval_value e interval_unit de la tabla.
func scheduleArgs(s cron.Schedule) (string, *int, *string) {
	if s.Type == cron.TypeInterval {
		return "", &s.Every, &s.Unit
	}
	return s.Crontab, nil, nil
}

// checkInIDColumn da el check_in_id en 32 hex, como lo mandan los SDK.
const checkInIDColumn = `replace(CAST(check_in_id AS text), '-', '')`

const checkInColumns = `id, monitor_id, ` + checkInIDColumn + `, status, duration_ms, environment, release,
	expected_at, started_at, finished_at, timeout_at, received_at`

func scanCheckIn(r db.Row) (CheckIn, error) {
	var c CheckIn
	err := r.Scan(&c.ID, &c.MonitorID, &c.CheckInID, &c.Status, &c.DurationMs, &c.Environment, &c.Release,
		&c.ExpectedAt, &c.StartedAt, &c.FinishedAt, &c.TimeoutAt, &c.ReceivedAt)
	return c, err
}

// MonitorFilter elige los monitores de la lista.
type MonitorFilter struct {
	// Project es el id o el nombre; vacío, todos.
	Project string
	// Status es ok, error, missed, timeout o pending; vacío, todos.
	Status string
}

// ListMonitors da los monitores con los que fallan primero y, en cada uno,
// sus últimos check-ins.
func (s *Store) ListMonitors(ctx context.Context, f MonitorFilter) ([]Monitor, error) {
	var args []any
	where := []string{"true"}
	if f.Project != "" {
		p, err := s.ProjectByRef(ctx, f.Project)
		if err != nil {
			return nil, err
		}
		args = append(args, p.ID)
		where = append(where, "m.project_id = $"+strconv.Itoa(len(args)))
	}
	if f.Status != "" {
		switch f.Status {
		case MonitorPending, CheckInOK, CheckInError, CheckInMissed, CheckInTimeout:
		default:
			return nil, fmt.Errorf("%w: status es ok, error, missed, timeout o pending", ErrInvalid)
		}
		args = append(args, f.Status)
		where = append(where, "m.status = $"+strconv.Itoa(len(args)))
	}
	rows, err := s.pool.Query(ctx, `SELECT `+monitorColumns+monitorFrom+`
		WHERE `+strings.Join(where, " AND ")+`
		ORDER BY m.status IN ('error', 'missed', 'timeout') DESC, p.name, m.slug`, args...)
	if err != nil {
		return nil, fmt.Errorf("leer monitores: %w", err)
	}
	out, err := db.CollectRows(rows, func(r db.CollectableRow) (Monitor, error) { return scanMonitor(r) })
	if err != nil {
		return nil, fmt.Errorf("leer monitores: %w", err)
	}
	if len(out) == 0 {
		return []Monitor{}, nil
	}
	ids := make([]int64, len(out))
	idx := map[int64]int{}
	for i, m := range out {
		ids[i] = m.ID
		idx[m.ID] = i
		out[i].Recent = []CheckInMark{}
	}
	rows, err = s.pool.Query(ctx, `
		SELECT c.monitor_id, c.status, c.started_at, c.duration_ms
		FROM `+s.pool.Dialect().Unnest("m", 1, "id bigint")+`
		CROSS JOIN LATERAL (SELECT monitor_id, status, started_at, duration_ms FROM monitor_checkins
			WHERE monitor_id = m.id ORDER BY started_at DESC, id DESC LIMIT $2) c`, ids, RecentCheckIns)
	if err != nil {
		return nil, fmt.Errorf("leer check-ins: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var c CheckInMark
		if err := rows.Scan(&id, &c.Status, &c.StartedAt, &c.DurationMs); err != nil {
			return nil, fmt.Errorf("leer check-ins: %w", err)
		}
		out[idx[id]].Recent = append(out[idx[id]].Recent, c)
	}
	return out, rows.Err()
}

// Monitor busca el monitor por proyecto y slug.
func (s *Store) Monitor(ctx context.Context, projectID int64, slug string) (Monitor, error) {
	m, err := scanMonitor(s.pool.QueryRow(ctx, `SELECT `+monitorColumns+monitorFrom+`
		WHERE m.project_id = $1 AND m.slug = $2`, projectID, slug))
	if errors.Is(err, db.ErrNoRows) {
		return Monitor{}, ErrNotFound
	}
	if err != nil {
		return Monitor{}, fmt.Errorf("leer monitor: %w", err)
	}
	return m, nil
}

// MonitorByRef busca el monitor con el proyecto por id o nombre.
func (s *Store) MonitorByRef(ctx context.Context, project, slug string) (Monitor, error) {
	p, err := s.ProjectByRef(ctx, project)
	if err != nil {
		return Monitor{}, err
	}
	return s.Monitor(ctx, p.ID, slug)
}

// CheckInPage es una página del historial de un monitor, del más nuevo al más viejo.
type CheckInPage struct {
	CheckIns   []CheckIn `json:"data"`
	NextCursor string    `json:"next_cursor,omitempty"`
}

// CheckIns da el historial del monitor; cursor es el next_cursor de la página anterior.
func (s *Store) CheckIns(ctx context.Context, monitorID int64, cursor string, limit int) (CheckInPage, error) {
	if limit <= 0 {
		limit = 50
	}
	limit = min(limit, 200)
	var beforeID int64
	if cursor != "" {
		n, err := strconv.ParseInt(cursor, 10, 64)
		if err != nil || n <= 0 {
			return CheckInPage{}, fmt.Errorf("%w: cursor no válido", ErrInvalid)
		}
		beforeID = n
	}
	// El id sigue el orden de llegada; el historial va por started_at, que es
	// casi siempre el mismo, y el cursor es el (started_at, id) del último.
	q := `SELECT ` + checkInColumns + ` FROM monitor_checkins WHERE monitor_id = $1`
	args := []any{monitorID}
	if beforeID != 0 {
		q += ` AND (started_at, id) < (SELECT started_at, id FROM monitor_checkins WHERE id = $2 AND monitor_id = $1)`
		args = append(args, beforeID)
	}
	q += ` ORDER BY started_at DESC, id DESC LIMIT ` + strconv.Itoa(limit+1)
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return CheckInPage{}, fmt.Errorf("leer check-ins: %w", err)
	}
	list, err := db.CollectRows(rows, func(r db.CollectableRow) (CheckIn, error) { return scanCheckIn(r) })
	if err != nil {
		return CheckInPage{}, fmt.Errorf("leer check-ins: %w", err)
	}
	page := CheckInPage{CheckIns: list}
	if len(list) > limit {
		page.CheckIns = list[:limit]
		page.NextCursor = strconv.FormatInt(list[limit-1].ID, 10)
	}
	if page.CheckIns == nil {
		page.CheckIns = []CheckIn{}
	}
	return page, nil
}

// CreateMonitor crea un monitor a mano.
func (s *Store) CreateMonitor(ctx context.Context, projectID int64, in MonitorInput) (Monitor, error) {
	m := Monitor{CheckinMargin: DefaultCheckinMargin, MaxRuntime: DefaultMaxRuntime}
	if in.Slug == nil && in.Name != nil {
		slug := Slugify(*in.Name)
		in.Slug = &slug
	}
	if err := in.apply(&m); err != nil {
		return Monitor{}, err
	}
	sched, every, unit := scheduleArgs(m.Schedule)
	var id int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO monitors (project_id, slug, name, schedule_type, schedule, interval_value, interval_unit,
			timezone, checkin_margin, max_runtime, environment, muted)
		SELECT id, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12 FROM projects WHERE id = $1
		ON CONFLICT (project_id, slug) DO NOTHING
		RETURNING id`, projectID, m.Slug, m.Name, m.Schedule.Type, sched, every, unit,
		m.Timezone, m.CheckinMargin, m.MaxRuntime, m.Environment, m.Muted).Scan(&id)
	if errors.Is(err, db.ErrNoRows) {
		if _, err := s.ProjectByID(ctx, projectID); err != nil {
			return Monitor{}, err
		}
		return Monitor{}, fmt.Errorf("%w: ya hay un monitor %q en el proyecto", ErrInvalid, m.Slug)
	}
	if err != nil {
		return Monitor{}, fmt.Errorf("guardar monitor: %w", err)
	}
	return s.Monitor(ctx, projectID, m.Slug)
}

// UpdateMonitor cambia los campos que trae in. Con otro horario, la siguiente
// ejecución se calcula desde el último check-in.
func (s *Store) UpdateMonitor(ctx context.Context, projectID int64, slug string, in MonitorInput) (Monitor, error) {
	var out Monitor
	err := db.BeginFunc(ctx, s.pool, func(tx db.Tx) error {
		m, err := lockMonitor(ctx, tx, projectID, slug)
		if err != nil {
			return err
		}
		old := m
		if err := in.apply(&m); err != nil {
			return err
		}
		if (m.Schedule != old.Schedule || m.Timezone != old.Timezone) && m.LastCheckinAt != nil {
			next := m.Schedule.Next(*m.LastCheckinAt, m.Location())
			m.NextCheckinAt = &next
		}
		sched, every, unit := scheduleArgs(m.Schedule)
		_, err = tx.Exec(ctx, `
			UPDATE monitors SET slug = $2, name = $3, schedule_type = $4, schedule = $5, interval_value = $6,
				interval_unit = $7, timezone = $8, checkin_margin = $9, max_runtime = $10, environment = $11,
				muted = $12, next_checkin_at = $13, updated_at = now()
			WHERE id = $1`, m.ID, m.Slug, m.Name, m.Schedule.Type, sched, every, unit, m.Timezone,
			m.CheckinMargin, m.MaxRuntime, m.Environment, m.Muted, m.NextCheckinAt)
		var pe interface{ SQLState() string }
		if errors.As(err, &pe) && pe.SQLState() == "23505" {
			return fmt.Errorf("%w: ya hay un monitor %q en el proyecto", ErrInvalid, m.Slug)
		}
		out = m
		return err
	})
	if err != nil {
		return Monitor{}, err
	}
	return s.Monitor(ctx, projectID, out.Slug)
}

// DeleteMonitor borra el monitor con su historial y sus avisos pendientes.
func (s *Store) DeleteMonitor(ctx context.Context, projectID int64, slug string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM monitors WHERE project_id = $1 AND slug = $2`, projectID, slug)
	if err != nil {
		return fmt.Errorf("borrar monitor: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func lockMonitor(ctx context.Context, tx db.Tx, projectID int64, slug string) (Monitor, error) {
	m, err := scanMonitor(tx.QueryRow(ctx, `SELECT `+monitorColumns+monitorFrom+`
		WHERE m.project_id = $1 AND m.slug = $2`+tx.Dialect().ForUpdate("m"), projectID, slug))
	if errors.Is(err, db.ErrNoRows) {
		return Monitor{}, ErrNotFound
	}
	return m, err
}

// PurgeCheckIns borra los check-ins recibidos antes de cutoff, menos el último
// de cada monitor, que sostiene su estado.
func (s *Store) PurgeCheckIns(ctx context.Context, cutoff time.Time) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM monitor_checkins c WHERE c.received_at < $1
			AND c.id <> (SELECT max(id) FROM monitor_checkins l WHERE l.monitor_id = c.monitor_id)`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("borrar check-ins: %w", err)
	}
	return tag.RowsAffected(), nil
}

func newCheckInID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return hex.EncodeToString(b)
}

// ProjectByRef busca el proyecto por id o, si no lo hay, por nombre.
func (s *Store) ProjectByRef(ctx context.Context, ref string) (Project, error) {
	if id, err := strconv.ParseInt(ref, 10, 64); err == nil {
		if p, err := s.ProjectByID(ctx, id); !errors.Is(err, ErrNotFound) {
			return p, err
		}
	}
	return s.ProjectByName(ctx, ref)
}
