package ingest

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/intake-es/intake-errors/internal/db"
	"github.com/intake-es/intake-errors/internal/event"
	"github.com/intake-es/intake-errors/internal/search"
	"github.com/intake-es/intake-errors/internal/store"
)

// maxEventTags es cuántas tags propias del evento se cuentan como mucho.
const maxEventTags = 50

// Tag es un par clave-valor que se cuenta en el issue.
type Tag struct{ Key, Value string }

// Tags da las etiquetas de un evento, sin claves repetidas y ordenadas por
// clave: release, environment, level, las de contexts o, si faltan, las del
// user agent, y las tags del evento. Las derivadas ganan a una tag con su nombre.
func Tags(ev *event.Event) []Tag {
	m := map[string]string{}
	set := func(k, v string) {
		if v = strings.TrimSpace(v); v != "" {
			if _, ok := m[k]; !ok {
				m[k] = cut(v, search.MaxValueLen)
			}
		}
	}
	set("release", ev.Release)
	set("environment", ev.Environment)
	set("level", ev.Level)

	var ctx struct {
		Browser struct{ Name string } `json:"browser"`
		OS      struct{ Name string } `json:"os"`
		Device  struct {
			Model, Family string
		} `json:"device"`
		Runtime struct{ Name string } `json:"runtime"`
	}
	_ = json.Unmarshal(ev.Contexts, &ctx)
	ua := UserAgent{}
	if ev.Request != nil {
		ua = ParseUserAgent(ev.Request.UserAgent)
	}
	set("browser", firstOf(ctx.Browser.Name, ua.Browser))
	set("os", firstOf(ctx.OS.Name, ua.OS))
	set("device", firstOf(ctx.Device.Model, ctx.Device.Family, ua.Device))
	set("runtime", ctx.Runtime.Name)

	keys := make([]string, 0, len(ev.Tags))
	for k := range ev.Tags {
		if search.ValidKey(k) && !slices.Contains(search.Keys, strings.ToLower(k)) {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	for _, k := range keys[:min(len(keys), maxEventTags)] {
		set(k, ev.Tags[k])
	}

	out := make([]Tag, 0, len(m))
	for k, v := range m {
		out = append(out, Tag{k, v})
	}
	slices.SortFunc(out, func(a, b Tag) int { return strings.Compare(a.Key, b.Key) })
	return out
}

func firstOf(vs ...string) string {
	for _, v := range vs {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// cut corta sin «…»: el valor guardado es el que se busca.
func cut(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// recordTags cuenta las etiquetas y el usuario del evento en el issue, en una
// sola sentencia. Va dentro de la transacción del evento, tras saber que no
// es un reenvío, para que un evento repetido no cuente dos veces.
func (in *Ingester) recordTags(ctx context.Context, tx db.Tx, issueID int64, ev *event.Event) error {
	tags := Tags(ev)
	keys := make([]string, len(tags))
	values := make([]string, len(tags))
	for i, t := range tags {
		keys[i], values[i] = t.Key, t.Value
	}
	var userHash []byte
	if ident := store.UserIdent(ev.User); ident != "" {
		key, err := in.userHashKey(ctx, tx)
		if err != nil {
			return err
		}
		userHash = store.UserHash(key, ident)
	}
	_, err := tx.Exec(ctx, `
		WITH t AS (
			INSERT INTO issue_tag_values (issue_id, key, value, count, last_seen)
			SELECT $1, k, v, 1, $4 FROM `+tx.Dialect().Unnest("x", 2, "k text", "v text")+`
			ON CONFLICT (issue_id, key, value) DO UPDATE SET
				count = issue_tag_values.count + 1,
				last_seen = `+tx.Dialect().Greatest("issue_tag_values.last_seen", "EXCLUDED.last_seen")+`
		), u AS (
			INSERT INTO issue_users (issue_id, user_hash)
			SELECT $1, $5::bytea WHERE $5::bytea IS NOT NULL
			ON CONFLICT DO NOTHING
			RETURNING 1
		)
		UPDATE issues SET user_count = user_count + (SELECT count(*) FROM u)
		WHERE id = $1 AND EXISTS (SELECT 1 FROM u)`,
		issueID, keys, values, ev.Timestamp, userHash)
	if err != nil {
		return fmt.Errorf("contar etiquetas: %w", err)
	}
	return nil
}

func (in *Ingester) userHashKey(ctx context.Context, tx db.Tx) ([]byte, error) {
	if k := in.hashKey.Load(); k != nil {
		return *k, nil
	}
	k, err := store.LoadUserHashKey(ctx, tx)
	if err != nil {
		return nil, err
	}
	in.hashKey.Store(&k)
	return k, nil
}
