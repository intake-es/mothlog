package store

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/intake-es/mothlog/internal/db"
	"github.com/intake-es/mothlog/internal/netguard"
)

// Estados de un monitor de disponibilidad.
const (
	UptimePending = "pending"
	UptimeUp      = "up"
	UptimeDown    = "down"
)

// Tipos de aviso de un monitor de disponibilidad.
const (
	KindUptimeDown = "uptime_down"
	KindUptimeUp   = "uptime_up"
)

const (
	MinUptimeInterval        = 30
	MaxUptimeInterval        = 3600
	MaxUptimeTimeout         = 60
	MaxUptimeThreshold       = 10
	DefaultUptimeInterval    = 60
	DefaultUptimeTimeout     = 10
	DefaultUptimeThreshold   = 2
	DefaultUptimeStatus      = "200-299"
	maxUptimeURL             = 2000
	maxUptimeError           = 500
	maxStatusRanges          = 20
	RecentUptimeChecks       = 30
	uptimeDetailChecks       = 60
	uptimeDetailIncidents    = 50
	ResponseTimeBucket       = 15 * time.Minute
	ResponseTimeWindow       = 24 * time.Hour
	uptimeAvailabilityWindow = 30 * 24 * time.Hour
)

// UptimeMonitor comprueba una URL cada IntervalSeconds.
type UptimeMonitor struct {
	ID                  int64      `json:"id"`
	ProjectID           int64      `json:"project_id"`
	Project             string     `json:"project"`
	Slug                string     `json:"slug"`
	Name                string     `json:"name"`
	URL                 string     `json:"url"`
	Method              string     `json:"method"`
	IntervalSeconds     int        `json:"interval_seconds"`
	TimeoutSeconds      int        `json:"timeout_seconds"`
	ExpectedStatus      string     `json:"expected_status"`
	FailureThreshold    int        `json:"failure_threshold"`
	Paused              bool       `json:"paused"`
	Status              string     `json:"status"`
	ConsecutiveFailures int        `json:"consecutive_failures"`
	LastCheckedAt       *time.Time `json:"last_checked_at"`
	LastStatusCode      *int       `json:"last_status_code"`
	LastDurationMs      *int       `json:"last_duration_ms"`
	LastError           string     `json:"last_error"`
	NextCheckAt         time.Time  `json:"next_check_at"`
	CreatedAt           time.Time  `json:"created_at"`
	UpdatedAt           time.Time  `json:"updated_at"`
	// DownSince es el principio del incidente abierto.
	DownSince *time.Time `json:"down_since"`
	// Uptime24h y Uptime30d son el porcentaje del tiempo sin incidente; nil sin comprobaciones.
	Uptime24h *float64 `json:"uptime_24h"`
	Uptime30d *float64 `json:"uptime_30d"`
	// Recent son las últimas comprobaciones, de la más nueva a la más vieja; solo en la lista.
	Recent []UptimeCheck `json:"recent,omitempty"`

	firstFailureAt    *time.Time
	firstFailureError string
}

// UptimeCheck es una comprobación.
type UptimeCheck struct {
	CheckedAt  time.Time `json:"checked_at"`
	OK         bool      `json:"ok"`
	StatusCode *int      `json:"status_code"`
	DurationMs int       `json:"duration_ms"`
	Error      string    `json:"error,omitempty"`
}

// UptimeIncident es un tiempo caído: de su primer fallo a la primera comprobación buena.
type UptimeIncident struct {
	ID         int64      `json:"id"`
	MonitorID  int64      `json:"monitor_id"`
	StartedAt  time.Time  `json:"started_at"`
	EndedAt    *time.Time `json:"ended_at"`
	DurationMs *int64     `json:"duration_ms"`
	FirstError string     `json:"first_error"`
}

// ResponseTimes es un tramo de la gráfica de tiempos de respuesta.
type ResponseTimes struct {
	At       time.Time `json:"at"`
	AvgMs    *int      `json:"avg_ms"`
	MaxMs    int       `json:"max_ms"`
	Checks   int       `json:"checks"`
	Failures int       `json:"failures"`
}

// UptimeDetail es un monitor con sus incidentes, sus últimas comprobaciones y
// sus tiempos de respuesta de las últimas 24 h.
type UptimeDetail struct {
	UptimeMonitor
	Incidents     []UptimeIncident `json:"incidents"`
	Checks        []UptimeCheck    `json:"checks"`
	ResponseTimes []ResponseTimes  `json:"response_times"`
}

// UptimeInput crea o cambia un monitor; los campos nil no cambian.
type UptimeInput struct {
	Slug             *string `json:"slug"`
	Name             *string `json:"name"`
	URL              *string `json:"url"`
	Method           *string `json:"method"`
	IntervalSeconds  *int    `json:"interval_seconds"`
	TimeoutSeconds   *int    `json:"timeout_seconds"`
	ExpectedStatus   *string `json:"expected_status"`
	FailureThreshold *int    `json:"failure_threshold"`
	Paused           *bool   `json:"paused"`
}

// StatusRange son los códigos de From a To, los dos incluidos.
type StatusRange struct{ From, To int }

// ParseStatusCodes lee «200-299», «2xx,404» o «200,301-302» y devuelve los
// tramos con la forma limpia del texto.
func ParseStatusCodes(s string) ([]StatusRange, string, error) {
	var out []StatusRange
	var canon []string
	for part := range strings.SplitSeq(s, ",") {
		part = strings.ToLower(strings.TrimSpace(part))
		if part == "" {
			continue
		}
		var r StatusRange
		switch {
		case len(part) == 3 && strings.HasSuffix(part, "xx") && part[0] >= '1' && part[0] <= '5':
			n := int(part[0]-'0') * 100
			r = StatusRange{n, n + 99}
		case strings.Contains(part, "-"):
			a, b, _ := strings.Cut(part, "-")
			from, err1 := strconv.Atoi(strings.TrimSpace(a))
			to, err2 := strconv.Atoi(strings.TrimSpace(b))
			if err1 != nil || err2 != nil {
				return nil, "", fmt.Errorf("%w: %q is not a status code or a range of codes", ErrInvalid, part)
			}
			r = StatusRange{from, to}
		default:
			n, err := strconv.Atoi(part)
			if err != nil {
				return nil, "", fmt.Errorf("%w: %q is not a status code or a range of codes", ErrInvalid, part)
			}
			r = StatusRange{n, n}
		}
		if r.From < 100 || r.To > 599 || r.From > r.To {
			return nil, "", fmt.Errorf("%w: codes go from 100 to 599: %q", ErrInvalid, part)
		}
		out = append(out, r)
		if r.From == r.To {
			canon = append(canon, strconv.Itoa(r.From))
		} else {
			canon = append(canon, strconv.Itoa(r.From)+"-"+strconv.Itoa(r.To))
		}
	}
	if len(out) == 0 {
		return nil, "", fmt.Errorf("%w: missing the codes that count as up", ErrInvalid)
	}
	if len(out) > maxStatusRanges {
		return nil, "", fmt.Errorf("%w: allows up to %d codes or ranges", ErrInvalid, maxStatusRanges)
	}
	return out, strings.Join(canon, ","), nil
}

// StatusMatches dice si el código está en los tramos de spec.
func StatusMatches(spec string, code int) bool {
	ranges, _, err := ParseStatusCodes(spec)
	if err != nil {
		return false
	}
	for _, r := range ranges {
		if code >= r.From && code <= r.To {
			return true
		}
	}
	return false
}

var blockedHosts = map[string]bool{"metadata.google.internal": true, "metadata": true, "instance-data": true}

// CheckUptimeURL comprueba que la URL es http o https, sin usuario ni clave, y
// que no apunta a una dirección de metadatos ni link-local. Devuelve la URL limpia.
func CheckUptimeURL(s string) (string, error) {
	s = strings.TrimSpace(s)
	u, err := url.Parse(s)
	switch {
	case s == "" || err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "":
		return "", fmt.Errorf("%w: the URL must be http or https", ErrInvalid)
	case len(s) > maxUptimeURL:
		return "", fmt.Errorf("%w: the URL allows up to %d characters", ErrInvalid, maxUptimeURL)
	case u.User != nil:
		return "", fmt.Errorf("%w: the URL can't include a user or password", ErrInvalid)
	}
	if err := checkHost(u.Hostname()); err != nil {
		return "", err
	}
	u.Fragment, u.RawFragment = "", ""
	return u.String(), nil
}

// checkHost rechaza los nombres de metadatos de la nube y las direcciones
// escritas tal cual que netguard bloquea siempre.
func checkHost(hostname string) error {
	host := strings.ToLower(strings.TrimSuffix(hostname, "."))
	if blockedHosts[host] {
		return fmt.Errorf("%w: the URL points to the cloud metadata", ErrInvalid)
	}
	if a, err := netip.ParseAddr(host); err == nil && netguard.Classify(a) == netguard.Blocked {
		return fmt.Errorf("%w: the URL points to %s, a metadata, link-local or reserved address", ErrInvalid, a.Unmap())
	}
	return nil
}

func (in UptimeInput) apply(m *UptimeMonitor) error {
	if in.Slug != nil {
		m.Slug = strings.TrimSpace(*in.Slug)
	}
	if in.Name != nil {
		m.Name = strings.TrimSpace(*in.Name)
	}
	if in.URL != nil {
		m.URL = strings.TrimSpace(*in.URL)
	}
	if in.Method != nil {
		m.Method = strings.ToUpper(strings.TrimSpace(*in.Method))
	}
	if in.IntervalSeconds != nil {
		m.IntervalSeconds = *in.IntervalSeconds
	}
	if in.TimeoutSeconds != nil {
		m.TimeoutSeconds = *in.TimeoutSeconds
	}
	if in.ExpectedStatus != nil {
		m.ExpectedStatus = *in.ExpectedStatus
	}
	if in.FailureThreshold != nil {
		m.FailureThreshold = *in.FailureThreshold
	}
	if in.Paused != nil {
		m.Paused = *in.Paused
	}
	switch {
	case m.Slug == "" || len(m.Slug) > maxSlug || !reSlug.MatchString(m.Slug):
		return fmt.Errorf("%w: the slug is lowercase letters, digits, - and _, up to %d characters", ErrInvalid, maxSlug)
	case len([]rune(m.Name)) > maxMonitorName:
		return fmt.Errorf("%w: the name allows up to %d characters", ErrInvalid, maxMonitorName)
	case m.Method != "GET" && m.Method != "HEAD":
		return fmt.Errorf("%w: the method is GET or HEAD", ErrInvalid)
	case m.IntervalSeconds < MinUptimeInterval || m.IntervalSeconds > MaxUptimeInterval:
		return fmt.Errorf("%w: interval_seconds goes from %d to %d", ErrInvalid, MinUptimeInterval, MaxUptimeInterval)
	case m.TimeoutSeconds < 1 || m.TimeoutSeconds > MaxUptimeTimeout:
		return fmt.Errorf("%w: timeout_seconds goes from 1 to %d", ErrInvalid, MaxUptimeTimeout)
	case m.TimeoutSeconds > m.IntervalSeconds:
		return fmt.Errorf("%w: timeout_seconds can't exceed interval_seconds", ErrInvalid)
	case m.FailureThreshold < 1 || m.FailureThreshold > MaxUptimeThreshold:
		return fmt.Errorf("%w: failure_threshold goes from 1 to %d", ErrInvalid, MaxUptimeThreshold)
	}
	var err error
	if m.URL, err = CheckUptimeURL(m.URL); err != nil {
		return err
	}
	if _, m.ExpectedStatus, err = ParseStatusCodes(m.ExpectedStatus); err != nil {
		return err
	}
	if m.Name == "" {
		m.Name = m.Slug
	}
	return nil
}

const uptimeColumns = `m.id, m.project_id, p.name, m.slug, m.name, m.url, m.method, m.interval_seconds,
	m.timeout_seconds, m.expected_status, m.failure_threshold, m.paused, m.status, m.consecutive_failures,
	m.last_checked_at, m.last_status_code, m.last_duration_ms, m.last_error, m.next_check_at, m.created_at,
	m.updated_at, m.first_failure_at, COALESCE(m.first_failure_error, ''),
	(SELECT started_at FROM uptime_incidents i WHERE i.monitor_id = m.id AND i.ended_at IS NULL)`

const uptimeFrom = ` FROM uptime_monitors m JOIN projects p ON p.id = m.project_id`

func scanUptime(r db.Row) (UptimeMonitor, error) {
	var m UptimeMonitor
	err := r.Scan(&m.ID, &m.ProjectID, &m.Project, &m.Slug, &m.Name, &m.URL, &m.Method, &m.IntervalSeconds,
		&m.TimeoutSeconds, &m.ExpectedStatus, &m.FailureThreshold, &m.Paused, &m.Status, &m.ConsecutiveFailures,
		&m.LastCheckedAt, &m.LastStatusCode, &m.LastDurationMs, &m.LastError, &m.NextCheckAt, &m.CreatedAt,
		&m.UpdatedAt, &m.firstFailureAt, &m.firstFailureError, &m.DownSince)
	return m, err
}

// UptimeFilter elige los monitores de la lista.
type UptimeFilter struct {
	// Project es el id o el nombre; vacío, todos.
	Project string
	// Status es up, down, pending o paused; vacío, todos.
	Status string
}

// ListUptimeMonitors da los monitores, los caídos primero, con su
// disponibilidad y sus últimas comprobaciones.
func (s *Store) ListUptimeMonitors(ctx context.Context, f UptimeFilter, now time.Time) ([]UptimeMonitor, error) {
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
	switch f.Status {
	case "":
	case "paused":
		where = append(where, "m.paused")
	case UptimeUp, UptimeDown, UptimePending:
		args = append(args, f.Status)
		where = append(where, "m.status = $"+strconv.Itoa(len(args))+" AND NOT m.paused")
	default:
		return nil, fmt.Errorf("%w: status is up, down, pending or paused", ErrInvalid)
	}
	rows, err := s.pool.Query(ctx, `SELECT `+uptimeColumns+uptimeFrom+`
		WHERE `+strings.Join(where, " AND ")+`
		ORDER BY m.status = 'down' AND NOT m.paused DESC, `+s.pool.Dialect().Collate("p.name")+`, `+s.pool.Dialect().Collate("m.name")+`, `+s.pool.Dialect().Collate("m.slug"), args...)
	if err != nil {
		return nil, fmt.Errorf("read uptime monitors: %w", err)
	}
	out, err := db.CollectRows(rows, func(r db.CollectableRow) (UptimeMonitor, error) { return scanUptime(r) })
	if err != nil {
		return nil, fmt.Errorf("read uptime monitors: %w", err)
	}
	if len(out) == 0 {
		return []UptimeMonitor{}, nil
	}
	ids := make([]int64, len(out))
	idx := map[int64]int{}
	for i, m := range out {
		ids[i] = m.ID
		idx[m.ID] = i
		out[i].Recent = []UptimeCheck{}
	}
	if err := s.fillAvailability(ctx, out, idx, ids, now); err != nil {
		return nil, err
	}
	d := s.pool.Dialect()
	rows, err = s.pool.Query(ctx, db.ByEngine(d, `
		SELECT c.monitor_id, c.checked_at, c.ok, c.status_code, c.duration_ms, c.error
		FROM `+d.Unnest("m", 1, "id bigint")+`
		CROSS JOIN LATERAL (SELECT monitor_id, checked_at, ok, status_code, duration_ms, error FROM uptime_checks
			WHERE monitor_id = m.id ORDER BY checked_at DESC, id DESC LIMIT $2) c`,
		// Sin LATERAL: los ids de las últimas de cada monitor, por su índice.
		`
		SELECT c.monitor_id, c.checked_at, c.ok, c.status_code, c.duration_ms, c.error
		FROM `+d.Unnest("m", 1, "id bigint")+`
		JOIN uptime_checks c ON c.id IN (SELECT id FROM uptime_checks
			WHERE monitor_id = m.id ORDER BY checked_at DESC, id DESC LIMIT $2)
		ORDER BY c.monitor_id, c.checked_at DESC, c.id DESC`), ids, RecentUptimeChecks)
	if err != nil {
		return nil, fmt.Errorf("read checks: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var c UptimeCheck
		if err := rows.Scan(&id, &c.CheckedAt, &c.OK, &c.StatusCode, &c.DurationMs, &c.Error); err != nil {
			return nil, fmt.Errorf("read checks: %w", err)
		}
		out[idx[id]].Recent = append(out[idx[id]].Recent, c)
	}
	return out, rows.Err()
}

type span struct{ from, to time.Time }

// fillAvailability calcula la disponibilidad con los incidentes: los checks se
// guardan pocos días, los incidentes no.
func (s *Store) fillAvailability(ctx context.Context, list []UptimeMonitor, idx map[int64]int, ids []int64, now time.Time) error {
	rows, err := s.pool.Query(ctx, `
		SELECT monitor_id, started_at, ended_at FROM uptime_incidents
		WHERE `+s.pool.Dialect().InList("monitor_id", "$1")+` AND (ended_at IS NULL OR ended_at > $2)`, ids, now.Add(-uptimeAvailabilityWindow))
	if err != nil {
		return fmt.Errorf("read incidents: %w", err)
	}
	down := map[int64][]span{}
	for rows.Next() {
		var id int64
		var sp span
		var end *time.Time
		if err := rows.Scan(&id, &sp.from, &end); err != nil {
			rows.Close()
			return fmt.Errorf("read incidents: %w", err)
		}
		sp.to = now
		if end != nil {
			sp.to = *end
		}
		down[id] = append(down[id], sp)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read incidents: %w", err)
	}
	for _, id := range ids {
		m := &list[idx[id]]
		if m.LastCheckedAt == nil {
			continue
		}
		m.Uptime24h = availability(m.CreatedAt, down[id], now, 24*time.Hour)
		m.Uptime30d = availability(m.CreatedAt, down[id], now, uptimeAvailabilityWindow)
	}
	return nil
}

// availability es el porcentaje de la ventana que acaba en now, desde que
// existe el monitor, que queda fuera de los incidentes.
func availability(created time.Time, incidents []span, now time.Time, window time.Duration) *float64 {
	start := now.Add(-window)
	if created.After(start) {
		start = created
	}
	total := now.Sub(start)
	if total <= 0 {
		v := 100.0
		return &v
	}
	var down time.Duration
	for _, sp := range incidents {
		from, to := sp.from, sp.to
		if from.Before(start) {
			from = start
		}
		if to.After(now) {
			to = now
		}
		if to.After(from) {
			down += to.Sub(from)
		}
	}
	v := 100 * (1 - float64(down)/float64(total))
	v = max(0, min(100, v))
	return &v
}

// UptimeMonitor busca el monitor por proyecto y slug.
func (s *Store) UptimeMonitor(ctx context.Context, projectID int64, slug string) (UptimeMonitor, error) {
	m, err := scanUptime(s.pool.QueryRow(ctx, `SELECT `+uptimeColumns+uptimeFrom+`
		WHERE m.project_id = $1 AND m.slug = $2`, projectID, slug))
	if errors.Is(err, db.ErrNoRows) {
		return UptimeMonitor{}, ErrNotFound
	}
	if err != nil {
		return UptimeMonitor{}, fmt.Errorf("read uptime monitor: %w", err)
	}
	return m, nil
}

// UptimeMonitorByRef busca el monitor con el proyecto por id o nombre.
func (s *Store) UptimeMonitorByRef(ctx context.Context, project, slug string) (UptimeMonitor, error) {
	p, err := s.ProjectByRef(ctx, project)
	if err != nil {
		return UptimeMonitor{}, err
	}
	return s.UptimeMonitor(ctx, p.ID, slug)
}

// UptimeDetail da el monitor con sus incidentes, sus últimas comprobaciones y
// los tiempos de respuesta de las últimas 24 h en tramos de 15 min.
func (s *Store) UptimeDetail(ctx context.Context, m UptimeMonitor, now time.Time) (UptimeDetail, error) {
	list := []UptimeMonitor{m}
	if err := s.fillAvailability(ctx, list, map[int64]int{m.ID: 0}, []int64{m.ID}, now); err != nil {
		return UptimeDetail{}, err
	}
	d := UptimeDetail{UptimeMonitor: list[0]}
	rows, err := s.pool.Query(ctx, `
		SELECT id, monitor_id, started_at, ended_at, duration_ms, first_error FROM uptime_incidents
		WHERE monitor_id = $1 ORDER BY started_at DESC, id DESC LIMIT $2`, m.ID, uptimeDetailIncidents)
	if err != nil {
		return d, fmt.Errorf("read incidents: %w", err)
	}
	if d.Incidents, err = db.CollectRows(rows, func(r db.CollectableRow) (UptimeIncident, error) {
		var i UptimeIncident
		err := r.Scan(&i.ID, &i.MonitorID, &i.StartedAt, &i.EndedAt, &i.DurationMs, &i.FirstError)
		return i, err
	}); err != nil {
		return d, fmt.Errorf("read incidents: %w", err)
	}
	rows, err = s.pool.Query(ctx, `
		SELECT checked_at, ok, status_code, duration_ms, error FROM uptime_checks
		WHERE monitor_id = $1 ORDER BY checked_at DESC, id DESC LIMIT $2`, m.ID, uptimeDetailChecks)
	if err != nil {
		return d, fmt.Errorf("read checks: %w", err)
	}
	if d.Checks, err = db.CollectRows(rows, func(r db.CollectableRow) (UptimeCheck, error) {
		var c UptimeCheck
		err := r.Scan(&c.CheckedAt, &c.OK, &c.StatusCode, &c.DurationMs, &c.Error)
		return c, err
	}); err != nil {
		return d, fmt.Errorf("read checks: %w", err)
	}
	since := now.Add(-ResponseTimeWindow).Truncate(ResponseTimeBucket)
	// El tramo de cada comprobación; en SQLite, con las fechas y el paso en microsegundos.
	bin, step := `date_bin($3::interval, checked_at, $2)`, any(ResponseTimeBucket)
	if s.sqlite() {
		bin, step = `($2 + (checked_at - $2) / $3 * $3)`, ResponseTimeBucket.Microseconds()
	}
	rows, err = s.pool.Query(ctx, `
		SELECT `+bin+` AS at, CAST(round(avg(duration_ms) FILTER (WHERE ok)) AS integer),
			max(duration_ms), count(*), count(*) FILTER (WHERE NOT ok)
		FROM uptime_checks WHERE monitor_id = $1 AND checked_at >= $2 AND checked_at <= $4
		GROUP BY at ORDER BY at`, m.ID, since, step, now)
	if err != nil {
		return d, fmt.Errorf("read response times: %w", err)
	}
	if d.ResponseTimes, err = db.CollectRows(rows, func(r db.CollectableRow) (ResponseTimes, error) {
		var b ResponseTimes
		err := r.Scan(&b.At, &b.AvgMs, &b.MaxMs, &b.Checks, &b.Failures)
		return b, err
	}); err != nil {
		return d, fmt.Errorf("read response times: %w", err)
	}
	if d.Incidents == nil {
		d.Incidents = []UptimeIncident{}
	}
	if d.Checks == nil {
		d.Checks = []UptimeCheck{}
	}
	if d.ResponseTimes == nil {
		d.ResponseTimes = []ResponseTimes{}
	}
	return d, nil
}

func uniqueViolation(err error) bool { return db.IsUniqueViolation(err) }

// CreateUptimeMonitor crea un monitor; se comprueba por primera vez en el siguiente repaso.
func (s *Store) CreateUptimeMonitor(ctx context.Context, projectID int64, in UptimeInput) (UptimeMonitor, error) {
	m := UptimeMonitor{Method: "GET", IntervalSeconds: DefaultUptimeInterval, TimeoutSeconds: DefaultUptimeTimeout,
		ExpectedStatus: DefaultUptimeStatus, FailureThreshold: DefaultUptimeThreshold}
	if in.Slug == nil && in.Name != nil {
		slug := Slugify(*in.Name)
		in.Slug = &slug
	}
	if err := in.apply(&m); err != nil {
		return UptimeMonitor{}, err
	}
	var id int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO uptime_monitors (project_id, slug, name, url, method, interval_seconds, timeout_seconds,
			expected_status, failure_threshold, paused)
		SELECT id, $2, $3, $4, $5, $6, $7, $8, $9, $10 FROM projects WHERE id = $1
		RETURNING id`, projectID, m.Slug, m.Name, m.URL, m.Method, m.IntervalSeconds, m.TimeoutSeconds,
		m.ExpectedStatus, m.FailureThreshold, m.Paused).Scan(&id)
	if errors.Is(err, db.ErrNoRows) {
		return UptimeMonitor{}, ErrNotFound
	}
	if uniqueViolation(err) {
		return UptimeMonitor{}, fmt.Errorf("%w: there is already an uptime monitor %q in the project", ErrInvalid, m.Slug)
	}
	if err != nil {
		return UptimeMonitor{}, fmt.Errorf("save uptime monitor: %w", err)
	}
	return s.UptimeMonitor(ctx, projectID, m.Slug)
}

// UpdateUptimeMonitor cambia los campos que trae in. Con otra URL u otra forma
// de comprobarla, o al reanudarlo, se comprueba en el siguiente repaso. Al
// pausar uno caído, su incidente se cierra y vuelve a pending.
func (s *Store) UpdateUptimeMonitor(ctx context.Context, projectID int64, slug string, in UptimeInput, now time.Time) (UptimeMonitor, error) {
	var out UptimeMonitor
	err := db.BeginFunc(ctx, s.pool, func(tx db.Tx) error {
		m, err := scanUptime(tx.QueryRow(ctx, `SELECT `+uptimeColumns+uptimeFrom+`
			WHERE m.project_id = $1 AND m.slug = $2`+tx.Dialect().ForUpdate("m"), projectID, slug))
		if errors.Is(err, db.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		old := m
		if err := in.apply(&m); err != nil {
			return err
		}
		changed := m.URL != old.URL || m.Method != old.Method || m.ExpectedStatus != old.ExpectedStatus ||
			m.TimeoutSeconds != old.TimeoutSeconds
		next := m.NextCheckAt
		if changed || (old.Paused && !m.Paused) || m.IntervalSeconds < old.IntervalSeconds {
			next = now
		}
		failures, firstAt, firstErr := m.ConsecutiveFailures, m.firstFailureAt, nullIfEmpty(m.firstFailureError)
		if changed && m.Status != UptimeDown {
			failures, firstAt, firstErr = 0, nil, nil
		}
		// Al pausarlo, lo que no se vigila no cuenta como caído: el incidente
		// abierto se cierra a esa hora, sin aviso, y al reanudarlo empieza de cero.
		if m.Paused && !old.Paused {
			failures, firstAt, firstErr = 0, nil, nil
			if m.Status == UptimeDown {
				end := tx.Dialect().Greatest(tx.Dialect().Typed("$2", "timestamptz"), "started_at")
				if _, err := tx.Exec(ctx, `
					UPDATE uptime_incidents SET ended_at = `+end+`,
						duration_ms = CAST((`+tx.Dialect().SecondsBetween(end, "started_at")+` * 1000) AS bigint)
					WHERE monitor_id = $1 AND ended_at IS NULL`, m.ID, now.UTC()); err != nil {
					return fmt.Errorf("close incident: %w", err)
				}
				m.Status = UptimePending
			}
		}
		_, err = tx.Exec(ctx, `
			UPDATE uptime_monitors SET slug = $2, name = $3, url = $4, method = $5, interval_seconds = $6,
				timeout_seconds = $7, expected_status = $8, failure_threshold = $9, paused = $10, next_check_at = $11,
				consecutive_failures = $12, first_failure_at = $13, first_failure_error = $14, status = $15,
				updated_at = now()
			WHERE id = $1`, m.ID, m.Slug, m.Name, m.URL, m.Method, m.IntervalSeconds, m.TimeoutSeconds,
			m.ExpectedStatus, m.FailureThreshold, m.Paused, next, failures, firstAt, firstErr, m.Status)
		if uniqueViolation(err) {
			return fmt.Errorf("%w: there is already an uptime monitor %q in the project", ErrInvalid, m.Slug)
		}
		out = m
		return err
	})
	if err != nil {
		return UptimeMonitor{}, err
	}
	return s.UptimeMonitor(ctx, projectID, out.Slug)
}

func nullIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// DeleteUptimeMonitor borra el monitor con sus comprobaciones, sus incidentes y sus avisos pendientes.
func (s *Store) DeleteUptimeMonitor(ctx context.Context, projectID int64, slug string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM uptime_monitors WHERE project_id = $1 AND slug = $2`, projectID, slug)
	if err != nil {
		return fmt.Errorf("delete uptime monitor: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// UptimeTarget es lo que hace falta para comprobar un monitor.
type UptimeTarget struct {
	ID             int64
	URL            string
	Method         string
	Timeout        time.Duration
	ExpectedStatus string
}

// ClaimUptimeChecks reserva hasta limit monitores a los que les toca y les
// pone la siguiente comprobación un intervalo después. Con FOR UPDATE SKIP
// LOCKED, dos réplicas a la vez nunca reservan el mismo.
func (s *Store) ClaimUptimeChecks(ctx context.Context, now time.Time, limit int) ([]UptimeTarget, error) {
	d := s.pool.Dialect()
	rows, err := s.pool.Query(ctx, `
		UPDATE uptime_monitors AS m SET next_check_at = `+d.AddSeconds("$1", "m.interval_seconds")+`
		FROM (SELECT id AS due_id FROM uptime_monitors WHERE NOT paused AND next_check_at <= $1
			ORDER BY next_check_at, id LIMIT $2`+d.ForUpdateSkipLocked()+`) due
		WHERE m.id = due.due_id
		RETURNING id, url, method, timeout_seconds, expected_status`, now.UTC(), limit)
	if err != nil {
		return nil, fmt.Errorf("reserve checks: %w", err)
	}
	return db.CollectRows(rows, func(r db.CollectableRow) (UptimeTarget, error) {
		var t UptimeTarget
		var secs int
		err := r.Scan(&t.ID, &t.URL, &t.Method, &secs, &t.ExpectedStatus)
		t.Timeout = time.Duration(secs) * time.Second
		return t, err
	})
}

// UptimeChange es una caída o una vuelta, que pueden avisar.
type UptimeChange struct {
	Monitor  UptimeMonitor
	Incident UptimeIncident
	Kind     string
}

// UptimeNotifier encola los avisos de una caída o una vuelta en la transacción que la guarda.
type UptimeNotifier interface {
	UptimeChanged(ctx context.Context, tx db.Tx, ch UptimeChange) error
}

// RecordUptimeCheck guarda la comprobación y, con ella, abre el incidente al
// llegar a failure_threshold fallos seguidos o lo cierra con la primera buena.
// Devuelve el aviso que da, o "".
func (s *Store) RecordUptimeCheck(ctx context.Context, monitorID int64, c UptimeCheck, notify UptimeNotifier) (string, error) {
	c.CheckedAt = c.CheckedAt.UTC()
	if r := []rune(c.Error); len(r) > maxUptimeError {
		c.Error = string(r[:maxUptimeError])
	}
	kind := ""
	err := db.BeginFunc(ctx, s.pool, func(tx db.Tx) error {
		kind = ""
		m, err := scanUptime(tx.QueryRow(ctx, `SELECT `+uptimeColumns+uptimeFrom+` WHERE m.id = $1`+tx.Dialect().ForUpdate("m"), monitorID))
		if errors.Is(err, db.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read uptime monitor: %w", err)
		}
		if m.Paused {
			return nil
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO uptime_checks (monitor_id, checked_at, ok, status_code, duration_ms, error)
			VALUES ($1, $2, $3, $4, $5, $6)`, m.ID, c.CheckedAt, c.OK, c.StatusCode, c.DurationMs, c.Error); err != nil {
			return fmt.Errorf("save check: %w", err)
		}
		var inc UptimeIncident
		if c.OK {
			if m.Status == UptimeDown {
				end := tx.Dialect().Greatest("$2", "started_at")
				err := tx.QueryRow(ctx, `
					UPDATE uptime_incidents SET ended_at = `+end+`,
						duration_ms = CAST((`+tx.Dialect().SecondsBetween(end, "started_at")+` * 1000) AS bigint)
					WHERE monitor_id = $1 AND ended_at IS NULL
					RETURNING id, monitor_id, started_at, ended_at, duration_ms, first_error`, m.ID, c.CheckedAt).
					Scan(&inc.ID, &inc.MonitorID, &inc.StartedAt, &inc.EndedAt, &inc.DurationMs, &inc.FirstError)
				if err == nil {
					kind = KindUptimeUp
				} else if !errors.Is(err, db.ErrNoRows) {
					return fmt.Errorf("close incident: %w", err)
				}
			}
			m.Status, m.ConsecutiveFailures, m.firstFailureAt, m.firstFailureError = UptimeUp, 0, nil, ""
		} else {
			m.ConsecutiveFailures++
			if m.firstFailureAt == nil {
				at := c.CheckedAt
				m.firstFailureAt, m.firstFailureError = &at, c.Error
			}
			if m.Status != UptimeDown && m.ConsecutiveFailures >= m.FailureThreshold {
				err := tx.QueryRow(ctx, `
					INSERT INTO uptime_incidents (monitor_id, started_at, first_error) VALUES ($1, $2, $3)
					RETURNING id, monitor_id, started_at, ended_at, duration_ms, first_error`,
					m.ID, *m.firstFailureAt, m.firstFailureError).
					Scan(&inc.ID, &inc.MonitorID, &inc.StartedAt, &inc.EndedAt, &inc.DurationMs, &inc.FirstError)
				if err != nil {
					return fmt.Errorf("open incident: %w", err)
				}
				m.Status, m.DownSince, kind = UptimeDown, &inc.StartedAt, KindUptimeDown
			}
		}
		at := c.CheckedAt
		m.LastCheckedAt, m.LastStatusCode, m.LastDurationMs, m.LastError = &at, c.StatusCode, &c.DurationMs, c.Error
		if _, err := tx.Exec(ctx, `
			UPDATE uptime_monitors SET status = $2, consecutive_failures = $3, first_failure_at = $4,
				first_failure_error = $5, last_checked_at = $6, last_status_code = $7, last_duration_ms = $8, last_error = $9
			WHERE id = $1`, m.ID, m.Status, m.ConsecutiveFailures, m.firstFailureAt, nullIfEmpty(m.firstFailureError),
			c.CheckedAt, c.StatusCode, c.DurationMs, c.Error); err != nil {
			return fmt.Errorf("update uptime monitor: %w", err)
		}
		if kind == "" || notify == nil {
			return nil
		}
		if kind == KindUptimeUp {
			m.DownSince = nil
		}
		return notify.UptimeChanged(ctx, tx, UptimeChange{Monitor: m, Incident: inc, Kind: kind})
	})
	return kind, err
}

// PurgeUptimeChecks borra las comprobaciones de antes de cutoff.
func (s *Store) PurgeUptimeChecks(ctx context.Context, cutoff time.Time) (int64, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM uptime_checks WHERE checked_at < $1`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("delete checks: %w", err)
	}
	return tag.RowsAffected(), nil
}
