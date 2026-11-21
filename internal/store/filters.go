package store

import (
	"context"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// DefaultEventsPerIssuePerHour es el tope de eventos guardados por issue y
// hora de un proyecto que no pone el suyo.
const DefaultEventsPerIssuePerHour = 100

const (
	maxFilterItems   = 100
	maxFilterItemLen = 1000
)

// Motivos por los que la ingesta descarta un evento.
const (
	DropMessage          = "message"
	DropExceptionType    = "exception_type"
	DropBrowserExtension = "browser_extension"
	DropWebCrawler       = "web_crawler"
	DropLocalhost        = "localhost"
	DropEnvironment      = "environment"
	DropRelease          = "release"
	DropRateLimit        = "rate_limit"
	DropIssueLimit       = "issue_limit"
	DropSampled          = "sampled"
)

var DropReasons = []string{
	DropMessage, DropExceptionType, DropBrowserExtension, DropWebCrawler, DropLocalhost,
	DropEnvironment, DropRelease, DropRateLimit, DropIssueLimit, DropSampled,
}

// Filters es lo que la ingesta descarta o limita de un proyecto.
type Filters struct {
	// Messages son expresiones regulares que se buscan en el mensaje y en el
	// tipo y valor de cada excepción.
	Messages []string `json:"messages"`
	// ExceptionTypes descarta el evento si alguna de sus excepciones es de uno
	// de estos tipos, con o sin el módulo delante.
	ExceptionTypes    []string `json:"exception_types"`
	BrowserExtensions bool     `json:"browser_extensions"`
	WebCrawlers       bool     `json:"web_crawlers"`
	Localhost         bool     `json:"localhost"`
	Environments      []string `json:"environments"`
	// Releases admite * como comodín.
	Releases []string `json:"releases"`
	// EventsPerIssuePerHour nil es DefaultEventsPerIssuePerHour.
	EventsPerIssuePerHour *int `json:"events_per_issue_per_hour"`
	// SampleRate es la parte de los eventos de issues que ya existen que se guarda.
	SampleRate float64 `json:"sample_rate"`
	// RateLimitPerMinute nil es RATE_LIMIT_PER_MINUTE.
	RateLimitPerMinute *int `json:"rate_limit_per_minute"`
}

func DefaultFilters() Filters {
	return Filters{Messages: []string{}, ExceptionTypes: []string{}, Environments: []string{}, Releases: []string{}, SampleRate: 1}
}

func (f Filters) PerIssuePerHour() int {
	if f.EventsPerIssuePerHour == nil {
		return DefaultEventsPerIssuePerHour
	}
	return *f.EventsPerIssuePerHour
}

// Normalize quita los espacios de los extremos y los repetidos de las listas,
// y comprueba que todo es válido.
func (f *Filters) Normalize() error {
	lists := []struct {
		name string
		v    *[]string
	}{
		{"messages", &f.Messages}, {"exception_types", &f.ExceptionTypes},
		{"environments", &f.Environments}, {"releases", &f.Releases},
	}
	for _, l := range lists {
		out := []string{}
		for _, s := range *l.v {
			if l.name != "messages" {
				s = strings.TrimSpace(s)
			}
			switch {
			case s == "":
				return fmt.Errorf("%w: %s no admite valores vacíos", ErrInvalid, l.name)
			case len(s) > maxFilterItemLen:
				return fmt.Errorf("%w: %s admite valores de hasta %d caracteres", ErrInvalid, l.name, maxFilterItemLen)
			}
			if !slices.Contains(out, s) {
				out = append(out, s)
			}
		}
		if len(out) > maxFilterItems {
			return fmt.Errorf("%w: %s admite hasta %d valores", ErrInvalid, l.name, maxFilterItems)
		}
		*l.v = out
	}
	for _, m := range f.Messages {
		if _, err := regexp.Compile(m); err != nil {
			return fmt.Errorf("%w: messages: %q no es una expresión regular válida: %v", ErrInvalid, m, err)
		}
	}
	if math.IsNaN(f.SampleRate) || f.SampleRate < 0 || f.SampleRate > 1 {
		return fmt.Errorf("%w: sample_rate va de 0 a 1", ErrInvalid)
	}
	if n := f.EventsPerIssuePerHour; n != nil && (*n <= 0 || *n > 1_000_000) {
		return fmt.Errorf("%w: events_per_issue_per_hour va de 1 a 1000000", ErrInvalid)
	}
	if n := f.RateLimitPerMinute; n != nil && (*n <= 0 || *n > 1_000_000) {
		return fmt.Errorf("%w: rate_limit_per_minute va de 1 a 1000000", ErrInvalid)
	}
	return nil
}

// Filters da los filtros del proyecto; los que no tienen fila, los de por defecto.
func (s *Store) Filters(ctx context.Context, projectID int64) (Filters, error) {
	f := DefaultFilters()
	err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(f.messages, '{}'), COALESCE(f.exception_types, '{}'),
			COALESCE(f.browser_extensions, false), COALESCE(f.web_crawlers, false), COALESCE(f.localhost, false),
			COALESCE(f.environments, '{}'), COALESCE(f.releases, '{}'),
			f.events_per_issue_per_hour, COALESCE(f.sample_rate, 1), f.rate_limit_per_minute
		FROM projects p LEFT JOIN project_filters f ON f.project_id = p.id
		WHERE p.id = $1`, projectID).Scan(&f.Messages, &f.ExceptionTypes, &f.BrowserExtensions, &f.WebCrawlers,
		&f.Localhost, &f.Environments, &f.Releases, &f.EventsPerIssuePerHour, &f.SampleRate, &f.RateLimitPerMinute)
	if errors.Is(err, pgx.ErrNoRows) {
		return Filters{}, ErrNotFound
	}
	if err != nil {
		return Filters{}, fmt.Errorf("leer filtros: %w", err)
	}
	return f, nil
}

// SetFilters sustituye los filtros del proyecto y devuelve cómo quedan.
func (s *Store) SetFilters(ctx context.Context, projectID int64, f Filters) (Filters, error) {
	if err := f.Normalize(); err != nil {
		return Filters{}, err
	}
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO project_filters (project_id, messages, exception_types, browser_extensions, web_crawlers,
			localhost, environments, releases, events_per_issue_per_hour, sample_rate, rate_limit_per_minute)
		SELECT id, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11 FROM projects WHERE id = $1
		ON CONFLICT (project_id) DO UPDATE SET
			messages = EXCLUDED.messages, exception_types = EXCLUDED.exception_types,
			browser_extensions = EXCLUDED.browser_extensions, web_crawlers = EXCLUDED.web_crawlers,
			localhost = EXCLUDED.localhost, environments = EXCLUDED.environments, releases = EXCLUDED.releases,
			events_per_issue_per_hour = EXCLUDED.events_per_issue_per_hour, sample_rate = EXCLUDED.sample_rate,
			rate_limit_per_minute = EXCLUDED.rate_limit_per_minute, updated_at = now()`,
		projectID, f.Messages, f.ExceptionTypes, f.BrowserExtensions, f.WebCrawlers, f.Localhost,
		f.Environments, f.Releases, f.EventsPerIssuePerHour, f.SampleRate, f.RateLimitPerMinute)
	if err != nil {
		return Filters{}, fmt.Errorf("guardar filtros: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return Filters{}, ErrNotFound
	}
	return f, nil
}

// Dropped es una cuenta de eventos descartados de un proyecto, un motivo y un día.
type Dropped struct {
	ProjectID int64
	Reason    string
	Day       time.Time
	Count     int64
}

// AddDropped suma las cuentas a las que ya hay. Las de proyectos que ya no
// existen se ignoran.
func (s *Store) AddDropped(ctx context.Context, ds []Dropped) error {
	if len(ds) == 0 {
		return nil
	}
	projects := make([]int64, len(ds))
	reasons := make([]string, len(ds))
	days := make([]time.Time, len(ds))
	counts := make([]int64, len(ds))
	for i, d := range ds {
		projects[i], reasons[i], days[i], counts[i] = d.ProjectID, d.Reason, d.Day, d.Count
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO dropped_events (project_id, reason, day, count)
		SELECT d.project_id, d.reason, (d.day AT TIME ZONE 'UTC')::date, sum(d.count)
		FROM unnest($1::bigint[], $2::text[], $3::timestamptz[], $4::bigint[]) AS d(project_id, reason, day, count)
		JOIN projects p ON p.id = d.project_id
		GROUP BY 1, 2, 3
		ON CONFLICT (project_id, day, reason) DO UPDATE SET count = dropped_events.count + EXCLUDED.count`,
		projects, reasons, days, counts)
	if err != nil {
		return fmt.Errorf("contar descartados: %w", err)
	}
	return nil
}

type DroppedDay struct {
	Date   string `json:"date"`
	Reason string `json:"reason"`
	Count  int64  `json:"count"`
}

type DroppedStats struct {
	// Since es el primer día (UTC) que se cuenta.
	Since string `json:"since"`
	Total int64  `json:"total"`
	// Reasons lleva todos los motivos, también los que están a cero.
	Reasons map[string]int64 `json:"reasons"`
	// Daily son los días y motivos con algo descartado, del más viejo al más nuevo.
	Daily []DroppedDay `json:"daily"`
}

// DroppedSince cuenta lo descartado del proyecto en los últimos days días (UTC), hoy incluido.
func (s *Store) DroppedSince(ctx context.Context, projectID int64, days int, now time.Time) (DroppedStats, error) {
	if days <= 0 || days > 3660 {
		return DroppedStats{}, fmt.Errorf("%w: days va de 1 a 3660", ErrInvalid)
	}
	if _, err := s.ProjectByID(ctx, projectID); err != nil {
		return DroppedStats{}, err
	}
	since := now.UTC().Truncate(24*time.Hour).AddDate(0, 0, 1-days)
	st := DroppedStats{Since: since.Format(time.DateOnly), Reasons: map[string]int64{}, Daily: []DroppedDay{}}
	for _, r := range DropReasons {
		st.Reasons[r] = 0
	}
	rows, err := s.pool.Query(ctx, `
		SELECT to_char(day, 'YYYY-MM-DD'), reason, count FROM dropped_events
		WHERE project_id = $1 AND day >= $2::date
		ORDER BY day, reason`, projectID, st.Since)
	if err != nil {
		return DroppedStats{}, fmt.Errorf("leer descartados: %w", err)
	}
	var d DroppedDay
	_, err = pgx.ForEachRow(rows, []any{&d.Date, &d.Reason, &d.Count}, func() error {
		st.Daily = append(st.Daily, d)
		st.Reasons[d.Reason] += d.Count
		st.Total += d.Count
		return nil
	})
	if err != nil {
		return DroppedStats{}, fmt.Errorf("leer descartados: %w", err)
	}
	return st, nil
}
