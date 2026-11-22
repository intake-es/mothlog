package store

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/intake-es/intake-errors/internal/event"
	"github.com/intake-es/intake-errors/internal/search"
)

// UserIdent es lo que identifica a un usuario, por este orden: id, email,
// username o IP. Vacío si el evento no trae usuario.
func UserIdent(u *event.User) string {
	if u == nil {
		return ""
	}
	switch {
	case u.ID != "":
		return "id:" + u.ID
	case strings.TrimSpace(u.Email) != "":
		return "email:" + strings.ToLower(strings.TrimSpace(u.Email))
	case u.Username != "":
		return "username:" + u.Username
	case u.IPAddress != "" && u.IPAddress != "{{auto}}":
		return "ip:" + u.IPAddress
	}
	return ""
}

// userIdents son las formas en que v puede identificar a un usuario, para user:.
func userIdents(v string) []string {
	return []string{"id:" + v, "email:" + strings.ToLower(strings.TrimSpace(v)), "username:" + v, "ip:" + v}
}

// UserHash es el hash que se guarda en lugar del usuario; la migración 00011
// lo calcula igual en SQL.
func UserHash(key []byte, ident string) []byte {
	h := sha256.New()
	h.Write(key)
	h.Write([]byte(ident))
	return h.Sum(nil)[:16]
}

type queryRower interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func LoadUserHashKey(ctx context.Context, q queryRower) ([]byte, error) {
	var k []byte
	if err := q.QueryRow(ctx, `SELECT key FROM user_hash_key`).Scan(&k); err != nil {
		return nil, fmt.Errorf("leer la clave de los usuarios: %w", err)
	}
	return k, nil
}

func (s *Store) userHashKey(ctx context.Context) ([]byte, error) {
	if k := s.hashKey.Load(); k != nil {
		return *k, nil
	}
	k, err := LoadUserHashKey(ctx, s.pool)
	if err != nil {
		return nil, err
	}
	s.hashKey.Store(&k)
	return k, nil
}

type TagValue struct {
	Value    string    `json:"value"`
	Count    int64     `json:"count"`
	Percent  float64   `json:"percent"`
	LastSeen time.Time `json:"last_seen"`
}

type TagDistribution struct {
	Key string `json:"key"`
	// Total es la suma de los recuentos de la clave: los eventos que la traían.
	Total int64 `json:"total"`
	// Distinct es cuántos valores distintos tiene, aunque solo vayan los primeros.
	Distinct int        `json:"distinct"`
	Values   []TagValue `json:"values"`
}

type IssueTags struct {
	IssueID   int64             `json:"issue_id"`
	Events    int64             `json:"events"`
	UserCount int64             `json:"user_count"`
	Tags      []TagDistribution `json:"tags"`
}

// MaxTagValues es cuántos valores de cada clave da IssueTags, los más frecuentes.
const MaxTagValues = 10

// IssueTags da la distribución de cada etiqueta del issue. El porcentaje es
// sobre los eventos que traían la clave, que pueden ser menos que los del issue.
func (s *Store) IssueTags(ctx context.Context, issueID int64) (IssueTags, error) {
	out := IssueTags{IssueID: issueID, Tags: []TagDistribution{}}
	err := s.pool.QueryRow(ctx, `SELECT event_count, user_count FROM issues WHERE id = $1`, issueID).
		Scan(&out.Events, &out.UserCount)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, ErrNotFound
	}
	if err != nil {
		return out, fmt.Errorf("leer issue: %w", err)
	}
	rows, err := s.pool.Query(ctx, `
		SELECT key, value, count, last_seen, sum(count) OVER (PARTITION BY key)
		FROM issue_tag_values WHERE issue_id = $1
		ORDER BY key, count DESC, value`, issueID)
	if err != nil {
		return out, fmt.Errorf("etiquetas: %w", err)
	}
	var key string
	var v TagValue
	var total int64
	_, err = pgx.ForEachRow(rows, []any{&key, &v.Value, &v.Count, &v.LastSeen, &total}, func() error {
		if n := len(out.Tags); n == 0 || out.Tags[n-1].Key != key {
			out.Tags = append(out.Tags, TagDistribution{Key: key, Total: total, Values: []TagValue{}})
		}
		d := &out.Tags[len(out.Tags)-1]
		d.Distinct++
		if len(d.Values) < MaxTagValues {
			v.Percent = float64(v.Count) * 100 / float64(total)
			d.Values = append(d.Values, v)
		}
		return nil
	})
	if err != nil {
		return out, fmt.Errorf("etiquetas: %w", err)
	}
	return out, nil
}

type TagKey struct {
	Key string `json:"key"`
	// Issues es en cuántos issues sale la clave.
	Issues int64 `json:"issues"`
}

type TagValueCount struct {
	Value string `json:"value"`
	// Issues es en cuántos issues sale el valor; Count, en cuántos eventos.
	Issues int64 `json:"issues"`
	Count  int64 `json:"count"`
}

const maxSuggestions = 20

// TagKeys son las claves de etiqueta que hay, para autocompletar, de la más usada a la menos.
func (s *Store) TagKeys(ctx context.Context, project string) ([]TagKey, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT t.key, count(DISTINCT t.issue_id) AS n
		FROM issue_tag_values t JOIN issues i ON i.id = t.issue_id JOIN projects p ON p.id = i.project_id
		WHERE $1 = '' OR p.id::text = $1 OR p.name = $1
		GROUP BY t.key ORDER BY n DESC, t.key LIMIT 100`, project)
	if err != nil {
		return nil, fmt.Errorf("claves de etiqueta: %w", err)
	}
	keys, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (TagKey, error) {
		var k TagKey
		return k, r.Scan(&k.Key, &k.Issues)
	})
	if err != nil {
		return nil, fmt.Errorf("claves de etiqueta: %w", err)
	}
	if keys == nil {
		keys = []TagKey{}
	}
	return keys, nil
}

// TagValues son los valores de una clave que empiezan por prefix, sin
// distinguir mayúsculas, del más frecuente al menos.
func (s *Store) TagValues(ctx context.Context, project, key, prefix string) ([]TagValueCount, error) {
	if !search.ValidKey(key) {
		return nil, fmt.Errorf("%w: key", ErrInvalid)
	}
	rows, err := s.pool.Query(ctx, `
		SELECT t.value, count(*) AS issues, sum(t.count)
		FROM issue_tag_values t JOIN issues i ON i.id = t.issue_id JOIN projects p ON p.id = i.project_id
		WHERE t.key = $1 AND ($2 = '' OR p.id::text = $2 OR p.name = $2)
		  AND t.value ILIKE $3
		GROUP BY t.value ORDER BY 3 DESC, t.value LIMIT $4`,
		key, project, prefixPattern(prefix), maxSuggestions)
	if err != nil {
		return nil, fmt.Errorf("valores de etiqueta: %w", err)
	}
	vals, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (TagValueCount, error) {
		var v TagValueCount
		return v, r.Scan(&v.Value, &v.Issues, &v.Count)
	})
	if err != nil {
		return nil, fmt.Errorf("valores de etiqueta: %w", err)
	}
	if vals == nil {
		vals = []TagValueCount{}
	}
	return vals, nil
}

func prefixPattern(p string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(p) + "%"
}
