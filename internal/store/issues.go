package store

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/intake-es/intake-errors/internal/event"
	"github.com/intake-es/intake-errors/internal/search"
)

// ErrInvalid envuelve los errores de parámetros que vienen del cliente.
var ErrInvalid = errors.New("parámetro no válido")

// ErrNoRelease: resolved_next sin release en el último evento.
var ErrNoRelease = errors.New("el último evento del issue no tiene release")

var Statuses = []string{"open", "resolved", "resolved_next", "silenced"}

const (
	DefaultLimit = 25
	MaxLimit     = 100
	// DailyDays son los días de la gráfica del detalle, hoy incluido.
	DailyDays = 14
	topValues = 5
)

func isUniqueViolation(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "23505"
}

func isForeignKeyViolation(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "23503"
}

type Issue struct {
	ID                int64      `json:"id"`
	ProjectID         int64      `json:"project_id"`
	Project           string     `json:"project"`
	Title             string     `json:"title"`
	Culprit           string     `json:"culprit"`
	Type              string     `json:"type"`
	Level             string     `json:"level"`
	Status            string     `json:"status"`
	Count             int64      `json:"count"`
	UserCount         int64      `json:"user_count"`
	FirstSeen         time.Time  `json:"first_seen"`
	LastSeen          time.Time  `json:"last_seen"`
	LastRelease       *string    `json:"last_release"`
	ResolvedInRelease *string    `json:"resolved_in_release"`
	LastRegression    *time.Time `json:"last_regression"`
	// FirstRelease es la release del primer evento.
	FirstRelease *string `json:"first_release"`
	// FixedInRelease es la release cuyo despliegue resolvió el issue que estaba en resolved_next.
	FixedInRelease *string `json:"fixed_in_release"`
	// Assignee es quien lo lleva; nil, nadie.
	Assignee *Assignee `json:"assignee"`
}

const issueColumns = `
	i.id, i.project_id, p.name, i.title, i.culprit, i.type, i.level, i.status, i.event_count, i.user_count,
	i.first_seen, i.last_seen,
	(SELECT e.release FROM events e
	  WHERE e.issue_id = i.id AND e.release IS NOT NULL AND e.release <> ''
	  ORDER BY e.occurred_at DESC, e.id DESC LIMIT 1),
	i.resolved_in_release, i.last_regressed_at, i.first_release, i.fixed_in_release`

const issueFrom = `issues i JOIN projects p ON p.id = i.project_id`

// issueSQL da las columnas y el FROM de un issue. Con entorno (envArg es su
// parámetro), los eventos, la primera y la última vez y la última release son
// los de ese entorno.
func issueSQL(envArg string) (cols, from string) {
	if envArg == "" {
		return issueColumns, issueFrom
	}
	cols = `
	i.id, i.project_id, p.name, i.title, i.culprit, i.type, i.level, i.status, es.n, i.user_count,
	es.first_seen, es.last_seen,
	(SELECT e.release FROM events e
	  WHERE e.issue_id = i.id AND e.environment = ` + envArg + ` AND e.release IS NOT NULL AND e.release <> ''
	  ORDER BY e.occurred_at DESC, e.id DESC LIMIT 1),
	i.resolved_in_release, i.last_regressed_at, i.first_release, i.fixed_in_release`
	from = issueFrom + ` CROSS JOIN LATERAL (
		SELECT COALESCE(sum(s.event_count), 0)::bigint AS n,
			COALESCE(min(s.first_seen), i.first_seen) AS first_seen,
			COALESCE(max(s.last_seen), i.last_seen) AS last_seen
		FROM issue_stats s WHERE s.issue_id = i.id AND s.environment = ` + envArg + `) es`
	return cols, from
}

// IssueFilter son los parámetros de la lista, iguales en la API y en el MCP.
type IssueFilter struct {
	// Project es el id o el nombre del proyecto; vacío, todos.
	Project string
	// Status: open, resolved, resolved_next, silenced o all. Vacío es open,
	// salvo que Query traiga is:, y entonces all.
	Status string
	// Query va en el lenguaje de internal/search.
	Query string
	// Environment deja los issues con eventos en ese entorno y cuenta solo los suyos; vacío, todos.
	Environment string
	// Sort: last_seen (por defecto), first_seen o count, siempre descendente.
	Sort   string
	Cursor string
	Limit  int
	// Viewer es quien busca, para assigned:me.
	Viewer *Actor
}

type IssuePage struct {
	Issues     []Issue `json:"data"`
	NextCursor string  `json:"next_cursor,omitempty"`
}

var sortColumns = map[string]string{
	"last_seen":  "i.last_seen",
	"first_seen": "i.first_seen",
	"count":      "i.event_count",
}

// envSortColumns ordena por los recuentos del entorno elegido.
var envSortColumns = map[string]string{
	"last_seen":  "es.last_seen",
	"first_seen": "es.first_seen",
	"count":      "es.n",
}

type cursor struct {
	Sort  string `json:"s"`
	Value string `json:"v"`
	ID    int64  `json:"id"`
}

func encodeCursor(c cursor) string {
	b, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeCursor(s, sort string) (cursor, error) {
	var c cursor
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err == nil {
		err = json.Unmarshal(b, &c)
	}
	if err != nil || c.Sort != sort || c.ID <= 0 {
		return c, fmt.Errorf("%w: cursor", ErrInvalid)
	}
	return c, nil
}

func normalizeLimit(n int) (int, error) {
	switch {
	case n == 0:
		return DefaultLimit, nil
	case n < 0 || n > MaxLimit:
		return 0, fmt.Errorf("%w: limit va de 1 a %d", ErrInvalid, MaxLimit)
	}
	return n, nil
}

func likePattern(q string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return "%" + r.Replace(q) + "%"
}

func (s *Store) ListIssues(ctx context.Context, f IssueFilter) (IssuePage, error) {
	if f.Sort == "" {
		f.Sort = "last_seen"
	}
	col, ok := sortColumns[f.Sort]
	if !ok {
		return IssuePage{}, fmt.Errorf("%w: sort es last_seen, first_seen o count", ErrInvalid)
	}
	query, err := search.Parse(f.Query)
	if err != nil {
		return IssuePage{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	if f.Status == "" {
		f.Status = "open"
		if len(query.Status()) > 0 {
			f.Status = "all"
		}
	}
	if f.Status != "all" && !slices.Contains(Statuses, f.Status) {
		return IssuePage{}, fmt.Errorf("%w: status es open, resolved, resolved_next, silenced o all", ErrInvalid)
	}
	limit, err := normalizeLimit(f.Limit)
	if err != nil {
		return IssuePage{}, err
	}

	where := []string{"true"}
	var args []any
	arg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}
	if f.Project != "" {
		where = append(where, fmt.Sprintf("(p.id::text = %s OR p.name = %[1]s)", arg(f.Project)))
	}
	envArg := ""
	if f.Environment != "" {
		envArg = arg(f.Environment)
		col = envSortColumns[f.Sort]
		where = append(where, "es.n > 0")
	}
	if f.Status != "all" {
		where = append(where, "i.status = "+arg(f.Status))
	}
	for _, t := range query.Terms {
		cond, err := s.termSQL(ctx, t, f.Viewer, arg)
		if err != nil {
			return IssuePage{}, err
		}
		if t.Negate {
			cond = "NOT " + cond
		}
		where = append(where, cond)
	}
	if f.Cursor != "" {
		c, err := decodeCursor(f.Cursor, f.Sort)
		if err != nil {
			return IssuePage{}, err
		}
		var v any
		if f.Sort == "count" {
			v, err = strconv.ParseInt(c.Value, 10, 64)
		} else {
			v, err = time.Parse(time.RFC3339Nano, c.Value)
		}
		if err != nil {
			return IssuePage{}, fmt.Errorf("%w: cursor", ErrInvalid)
		}
		where = append(where, fmt.Sprintf("(%s, i.id) < (%s, %s)", col, arg(v), arg(c.ID)))
	}
	cols, from := issueWithAssigneeSQL(envArg)
	q := fmt.Sprintf(`SELECT %s FROM %s
		WHERE %s ORDER BY %s DESC, i.id DESC LIMIT %d`,
		cols, from, strings.Join(where, " AND "), col, limit+1)
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return IssuePage{}, fmt.Errorf("listar issues: %w", err)
	}
	issues, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Issue, error) { return scanIssueWithAssignee(r) })
	if err != nil {
		return IssuePage{}, fmt.Errorf("listar issues: %w", err)
	}
	page := IssuePage{Issues: issues}
	if len(issues) > limit {
		page.Issues = issues[:limit]
		last := page.Issues[limit-1]
		c := cursor{Sort: f.Sort, ID: last.ID}
		switch f.Sort {
		case "count":
			c.Value = strconv.FormatInt(last.Count, 10)
		case "first_seen":
			c.Value = last.FirstSeen.Format(time.RFC3339Nano)
		default:
			c.Value = last.LastSeen.Format(time.RFC3339Nano)
		}
		page.NextCursor = encodeCursor(c)
	}
	if page.Issues == nil {
		page.Issues = []Issue{}
	}
	return page, nil
}

// termSQL es la condición de un término de la búsqueda, sin su negación.
func (s *Store) termSQL(ctx context.Context, t search.Term, viewer *Actor, arg func(any) string) (string, error) {
	switch t.Key {
	case "":
		return fmt.Sprintf("(i.title ILIKE %s OR i.culprit ILIKE %[1]s)", arg(likePattern(t.Value))), nil
	case "is":
		switch t.Value {
		case "regressed":
			return "(i.status = 'open' AND i.last_regressed_at IS NOT NULL)", nil
		case "assigned":
			return "(i.assignee IS NOT NULL)", nil
		case "unassigned":
			return "(i.assignee IS NULL)", nil
		}
		return "(i.status = " + arg(t.Value) + ")", nil
	case "assigned":
		// Con assignee vacío la condición es false, no NULL, para que !assigned: la deje pasar.
		if strings.EqualFold(t.Value, search.Me) {
			if viewer == nil {
				return "", fmt.Errorf("%w: assigned:me necesita saber quién busca", ErrInvalid)
			}
			if viewer.Kind == ActorToken {
				// El token activo con ese nombre es quien busca; los de uno revocado con el mismo nombre no son suyos.
				return fmt.Sprintf(`(i.assignee_token_id IS NOT NULL AND i.assignee_token_id =
					COALESCE((SELECT id FROM api_tokens WHERE name = %s AND revoked_at IS NULL), 0))`, arg(viewer.Name)), nil
			}
			return fmt.Sprintf("(i.assignee IS NOT NULL AND i.assignee_kind = %s AND i.assignee = %s)", arg(viewer.Kind), arg(viewer.Name)), nil
		}
		return fmt.Sprintf("(i.assignee IS NOT NULL AND lower(i.assignee) = lower(%s))", arg(t.Value)), nil
	case "project":
		return fmt.Sprintf("(p.id::text = %s OR p.name = %[1]s)", arg(t.Value)), nil
	case "user":
		key, err := s.userHashKey(ctx)
		if err != nil {
			return "", err
		}
		var hashes [][]byte
		for _, id := range userIdents(t.Value) {
			hashes = append(hashes, UserHash(key, id))
		}
		return "EXISTS (SELECT 1 FROM issue_users u WHERE u.issue_id = i.id AND u.user_hash = ANY(" + arg(hashes) + "))", nil
	}
	return fmt.Sprintf("EXISTS (SELECT 1 FROM issue_tag_values t WHERE t.issue_id = i.id AND t.key = %s AND t.value = %s)",
		arg(t.Key), arg(t.Value)), nil
}

func (s *Store) Issue(ctx context.Context, id int64) (Issue, error) {
	return s.IssueIn(ctx, id, "")
}

// IssueIn es Issue con los recuentos de un entorno; vacío, todos.
func (s *Store) IssueIn(ctx context.Context, id int64, env string) (Issue, error) {
	envArg, args := "", []any{id}
	if env != "" {
		envArg, args = "$2", append(args, env)
	}
	cols, from := issueWithAssigneeSQL(envArg)
	i, err := scanIssueWithAssignee(s.pool.QueryRow(ctx, `SELECT `+cols+` FROM `+from+` WHERE i.id = $1`, args...))
	if errors.Is(err, pgx.ErrNoRows) {
		return Issue{}, ErrNotFound
	}
	if err != nil {
		return Issue{}, fmt.Errorf("leer issue: %w", err)
	}
	return i, nil
}

// StoredEvent es un evento guardado con el evento normalizado entero.
type StoredEvent struct {
	ID         int64       `json:"id"`
	IssueID    int64       `json:"issue_id"`
	ProjectID  int64       `json:"project_id"`
	EventID    string      `json:"event_id"`
	OccurredAt time.Time   `json:"occurred_at"`
	ReceivedAt time.Time   `json:"received_at"`
	Event      event.Event `json:"event"`
}

const storedEventColumns = `id, issue_id, project_id, replace(event_id::text, '-', ''), occurred_at, received_at, data_zstd, data`

func scanStoredEvent(r pgx.Row) (StoredEvent, error) {
	var e StoredEvent
	var compressed, legacy []byte
	if err := r.Scan(&e.ID, &e.IssueID, &e.ProjectID, &e.EventID, &e.OccurredAt, &e.ReceivedAt, &compressed, &legacy); err != nil {
		return e, err
	}
	var err error
	if e.Event, err = decodeStored(compressed, legacy); err != nil {
		return e, fmt.Errorf("evento %d: %w", e.ID, err)
	}
	return e, nil
}

// Lo que se leía de events.data en SQL, solo para los eventos que aún no se
// han convertido. Se va cuando se quite la columna data.
const (
	legacyUserLabel = `COALESCE(NULLIF(e.data->'user'->>'id', ''), NULLIF(e.data->'user'->>'email', ''),
		NULLIF(e.data->'user'->>'username', ''), NULLIF(e.data->'user'->>'ip_address', ''))`
	legacyOrganizationID = `(e.data->'tags'->>'organization_id')`
	legacySummary        = `CASE WHEN jsonb_array_length(COALESCE(e.data->'exceptions', '[]')) > 0
		THEN concat_ws(': ', NULLIF(e.data->'exceptions'->-1->>'type', ''), NULLIF(e.data->'exceptions'->-1->>'value', ''))
		ELSE e.data->>'message' END`
)

// Event busca por el id interno o por el event_id del SDK (32 hex o UUID).
func (s *Store) Event(ctx context.Context, ref string) (StoredEvent, error) {
	var row pgx.Row
	if id, err := strconv.ParseInt(ref, 10, 64); err == nil {
		row = s.pool.QueryRow(ctx, `SELECT `+storedEventColumns+` FROM events WHERE id = $1`, id)
	} else if isUUID(ref) {
		row = s.pool.QueryRow(ctx, `SELECT `+storedEventColumns+` FROM events WHERE event_id = $1::uuid ORDER BY id LIMIT 1`, ref)
	} else {
		return StoredEvent{}, ErrNotFound
	}
	e, err := scanStoredEvent(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return StoredEvent{}, ErrNotFound
	}
	if err != nil {
		return StoredEvent{}, fmt.Errorf("leer evento: %w", err)
	}
	return e, nil
}

func isUUID(s string) bool {
	s = strings.ReplaceAll(s, "-", "")
	if len(s) != 32 {
		return false
	}
	for _, c := range s {
		if !strings.ContainsRune("0123456789abcdefABCDEF", c) {
			return false
		}
	}
	return true
}

func (s *Store) LatestEvent(ctx context.Context, issueID int64) (*StoredEvent, error) {
	return s.latestEventIn(ctx, issueID, "")
}

func (s *Store) latestEventIn(ctx context.Context, issueID int64, env string) (*StoredEvent, error) {
	e, err := scanStoredEvent(s.pool.QueryRow(ctx, `SELECT `+storedEventColumns+`
		FROM events WHERE issue_id = $1 AND ($2 = '' OR environment = $2)
		ORDER BY occurred_at DESC, id DESC LIMIT 1`, issueID, env))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("leer último evento: %w", err)
	}
	return &e, nil
}

type DayCount struct {
	Date  string `json:"date"`
	Count int64  `json:"count"`
}

type ValueCount struct {
	Value string `json:"value"`
	Count int64  `json:"count"`
}

type IssueDetail struct {
	Issue
	LatestEvent *StoredEvent `json:"latest_event"`
	// Daily son los eventos por día (UTC) de los últimos DailyDays días, del más viejo a hoy.
	Daily []DayCount `json:"daily"`
	// TopValues tiene environment, release, user y organization_id, cada uno con sus valores más frecuentes.
	TopValues map[string][]ValueCount `json:"top_values"`
	// Links son sus issues de Linear y sus issues y PRs de GitHub, con su estado allí.
	Links []IssueLink `json:"links"`
}

func (s *Store) IssueDetail(ctx context.Context, id int64, now time.Time) (IssueDetail, error) {
	return s.IssueDetailIn(ctx, id, "", now)
}

// IssueDetailIn es IssueDetail con solo los eventos de un entorno; vacío, todos.
func (s *Store) IssueDetailIn(ctx context.Context, id int64, env string, now time.Time) (IssueDetail, error) {
	issue, err := s.IssueIn(ctx, id, env)
	if err != nil {
		return IssueDetail{}, err
	}
	d := IssueDetail{Issue: issue}
	if d.LatestEvent, err = s.latestEventIn(ctx, id, env); err != nil {
		return IssueDetail{}, err
	}
	if d.Links, err = s.IssueLinks(ctx, id); err != nil {
		return IssueDetail{}, err
	}

	daily, err := s.DailyCountsIn(ctx, []int64{id}, env, now)
	if err != nil {
		return IssueDetail{}, err
	}
	d.Daily = daily[id]

	rows, err := s.pool.Query(ctx, `
		SELECT v.key, v.value, count(*) AS n
		FROM events e CROSS JOIN LATERAL (VALUES
			('environment', e.environment),
			('release', e.release),
			('user', COALESCE(e.user_label, `+legacyUserLabel+`)),
			('organization_id', COALESCE(e.organization_id, `+legacyOrganizationID+`))
		) AS v(key, value)
		WHERE e.issue_id = $1 AND v.value <> '' AND ($2 = '' OR e.environment = $2)
		GROUP BY v.key, v.value
		ORDER BY v.key, n DESC, v.value`, id, env)
	if err != nil {
		return IssueDetail{}, fmt.Errorf("valores frecuentes: %w", err)
	}
	d.TopValues = map[string][]ValueCount{"environment": {}, "release": {}, "user": {}, "organization_id": {}}
	var key string
	var vc ValueCount
	_, err = pgx.ForEachRow(rows, []any{&key, &vc.Value, &vc.Count}, func() error {
		if len(d.TopValues[key]) < topValues {
			d.TopValues[key] = append(d.TopValues[key], vc)
		}
		return nil
	})
	if err != nil {
		return IssueDetail{}, fmt.Errorf("valores frecuentes: %w", err)
	}
	return d, nil
}

// DailyCounts son los eventos por día (UTC) de cada issue en los últimos
// DailyDays días, del más viejo a hoy. Un id que no existe sale con ceros.
func (s *Store) DailyCounts(ctx context.Context, ids []int64, now time.Time) (map[int64][]DayCount, error) {
	return s.DailyCountsIn(ctx, ids, "", now)
}

// DailyCountsIn es DailyCounts con solo los eventos de un entorno; vacío, todos.
func (s *Store) DailyCountsIn(ctx context.Context, ids []int64, env string, now time.Time) (map[int64][]DayCount, error) {
	out := make(map[int64][]DayCount, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT i.id, to_char(d, 'YYYY-MM-DD'), count(e.id)
		FROM unnest($1::bigint[]) AS i(id)
		CROSS JOIN generate_series((($2::timestamptz AT TIME ZONE 'UTC')::date - $3::int + 1)::timestamp,
		                     ($2::timestamptz AT TIME ZONE 'UTC')::date::timestamp, interval '1 day') d
		LEFT JOIN events e ON e.issue_id = i.id
			AND e.occurred_at >= d AT TIME ZONE 'UTC'
			AND e.occurred_at < (d + interval '1 day') AT TIME ZONE 'UTC'
			AND ($4 = '' OR e.environment = $4)
		GROUP BY i.id, d ORDER BY i.id, d`, ids, now, DailyDays, env)
	if err != nil {
		return nil, fmt.Errorf("eventos por día: %w", err)
	}
	var id int64
	var c DayCount
	_, err = pgx.ForEachRow(rows, []any{&id, &c.Date, &c.Count}, func() error {
		out[id] = append(out[id], c)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("eventos por día: %w", err)
	}
	return out, nil
}

type EventSummary struct {
	ID          int64     `json:"id"`
	EventID     string    `json:"event_id"`
	OccurredAt  time.Time `json:"occurred_at"`
	ReceivedAt  time.Time `json:"received_at"`
	Release     string    `json:"release"`
	Environment string    `json:"environment"`
	User        string    `json:"user"`
	// Message es «Tipo: valor» de la excepción lanzada, o el mensaje si no hay excepción.
	Message string `json:"message"`
}

type EventPage struct {
	Events     []EventSummary `json:"data"`
	NextCursor string         `json:"next_cursor,omitempty"`
}

// IssueEvents lista los eventos del issue, del más nuevo al más viejo.
func (s *Store) IssueEvents(ctx context.Context, issueID int64, cur string, limit int) (EventPage, error) {
	return s.IssueEventsIn(ctx, issueID, "", cur, limit)
}

// IssueEventsIn es IssueEvents con solo los eventos de un entorno; vacío, todos.
func (s *Store) IssueEventsIn(ctx context.Context, issueID int64, env, cur string, limit int) (EventPage, error) {
	limit, err := normalizeLimit(limit)
	if err != nil {
		return EventPage{}, err
	}
	if _, err := s.Issue(ctx, issueID); err != nil {
		return EventPage{}, err
	}
	after := time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)
	afterID := int64(1<<63 - 1)
	if cur != "" {
		c, err := decodeCursor(cur, "events")
		if err != nil {
			return EventPage{}, err
		}
		if after, err = time.Parse(time.RFC3339Nano, c.Value); err != nil {
			return EventPage{}, fmt.Errorf("%w: cursor", ErrInvalid)
		}
		afterID = c.ID
	}
	rows, err := s.pool.Query(ctx, `
		SELECT e.id, replace(e.event_id::text, '-', ''), e.occurred_at, e.received_at,
			COALESCE(e.release, ''), COALESCE(e.environment, ''),
			COALESCE(e.user_label, `+legacyUserLabel+`, ''),
			COALESCE(e.summary, `+legacySummary+`, '')
		FROM events e
		WHERE e.issue_id = $1 AND (e.occurred_at, e.id) < ($2, $3) AND ($5 = '' OR e.environment = $5)
		ORDER BY e.occurred_at DESC, e.id DESC LIMIT $4`, issueID, after, afterID, limit+1, env)
	if err != nil {
		return EventPage{}, fmt.Errorf("listar eventos: %w", err)
	}
	events, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (EventSummary, error) {
		var e EventSummary
		err := r.Scan(&e.ID, &e.EventID, &e.OccurredAt, &e.ReceivedAt, &e.Release, &e.Environment, &e.User, &e.Message)
		return e, err
	})
	if err != nil {
		return EventPage{}, fmt.Errorf("listar eventos: %w", err)
	}
	page := EventPage{Events: events}
	if len(events) > limit {
		page.Events = events[:limit]
		last := page.Events[limit-1]
		page.NextCursor = encodeCursor(cursor{Sort: "events", Value: last.OccurredAt.Format(time.RFC3339Nano), ID: last.ID})
	}
	if page.Events == nil {
		page.Events = []EventSummary{}
	}
	return page, nil
}
