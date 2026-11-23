package ingest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pressly/goose/v3"

	"github.com/intake-es/intake-errors/internal/db"
	"github.com/intake-es/intake-errors/internal/event"
	"github.com/intake-es/intake-errors/internal/store"
	"github.com/intake-es/intake-errors/internal/store/storetest"
	"github.com/intake-es/intake-errors/migrations"
)

// La migración 00011 rellena las etiquetas y los usuarios de los eventos que
// ya había con las mismas reglas que la ingesta: cada evento, en su issue,
// debe dar lo mismo que Tags() y UserIdent().
func TestBackfillMatchesIngest(t *testing.T) {
	base := os.Getenv("INTAKE_ERRORS_TEST_DATABASE_URL")
	if base == "" {
		t.Skip("sin INTAKE_ERRORS_TEST_DATABASE_URL")
	}
	ctx := context.Background()
	schema := "test_" + storetest.RandomHex(t, 6)
	admin, err := db.OpenPostgres(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		admin.Close()
	})
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	pool, err := db.OpenPostgres(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	sqlDB := pool.SQLDB()
	t.Cleanup(func() { _ = sqlDB.Close() })
	p, err := goose.NewProvider(goose.DialectPostgres, sqlDB, migrations.FS,
		goose.WithSlog(slog.New(slog.NewTextHandler(io.Discard, nil))))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 9); err != nil {
		t.Fatal(err)
	}

	many := map[string]string{"A": "mayúscula primero", "browser": "de tag"}
	for i := range 60 {
		many[fmt.Sprintf("t%02d", i)] = "v"
	}
	events := []event.Event{
		{
			Level: "error", Release: " r1 ", Environment: "production",
			Contexts: json.RawMessage(`{"browser":{"name":"Brave"},"os":{"name":"  "},"device":{"model":" ","family":"Desktop"},"runtime":{"name":"CPython"}}`),
			Tags: map[string]string{
				"sentry:x": "1", "release": "de-tag", "level": "fatal", "browser": "de-tag", "os": "de-tag",
				"Is": "x", "user": "ana", "con espacio": "x", "1numero": "x", "org": " 7 ", "vacía": "", "blanca": "  ",
				"largo": strings.Repeat("ñ", 300), strings.Repeat("k", 33): "x",
			},
			User: &event.User{IPAddress: "{{auto}}"},
		},
		{Level: "warning", Tags: map[string]string{"browser": "Firefox", "device": "iPhone"}, User: &event.User{ID: "42", Email: "ana@x.es"}},
		{Level: "error", Tags: many, User: &event.User{Email: " Ana@X.es "}},
		{Level: "info", User: &event.User{Username: "ana", IPAddress: "1.2.3.4"}},
		{Level: "error", Environment: "staging", User: &event.User{IPAddress: "1.2.3.4"}},
	}

	var project int64
	if err := pool.QueryRow(ctx, `INSERT INTO projects (name, public_key) VALUES ('web', 'clave') RETURNING id`).Scan(&project); err != nil {
		t.Fatal(err)
	}
	issues := make([]int64, len(events))
	for i, ev := range events {
		if err := pool.QueryRow(ctx, `
			INSERT INTO issues (project_id, fingerprint, title, first_seen, last_seen, event_count)
			VALUES ($1, $2, 'x', now(), now(), 1) RETURNING id`, project, fmt.Sprint(i)).Scan(&issues[i]); err != nil {
			t.Fatal(err)
		}
		ev.ID = storetest.RandomHex(t, 16)
		ev.Timestamp = time.Now().UTC()
		data, err := json.Marshal(ev)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO events (issue_id, project_id, event_id, occurred_at, release, environment, data)
			VALUES ($1, $2, $3, $4, NULLIF($5, ''), NULLIF($6, ''), $7)`,
			issues[i], project, ev.ID, ev.Timestamp, ev.Release, ev.Environment, data); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := p.UpTo(ctx, 11); err != nil {
		t.Fatal(err)
	}
	key, err := store.LoadUserHashKey(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	for i := range events {
		ev := events[i]
		var want []string
		for _, tag := range Tags(&ev) {
			want = append(want, tag.Key+"="+tag.Value)
		}
		rows, err := pool.Query(ctx, `SELECT key || '=' || value FROM issue_tag_values
			WHERE issue_id = $1 AND count = 1 ORDER BY key COLLATE "C"`, issues[i])
		if err != nil {
			t.Fatal(err)
		}
		got, err := db.CollectRows(rows, db.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(got, want) {
			t.Errorf("evento %d:\nmigración %q\ningesta   %q", i, got, want)
		}

		var hashes [][]byte
		rows, err = pool.Query(ctx, `SELECT user_hash FROM issue_users WHERE issue_id = $1`, issues[i])
		if err != nil {
			t.Fatal(err)
		}
		if hashes, err = db.CollectRows(rows, db.RowTo[[]byte]); err != nil {
			t.Fatal(err)
		}
		var n int64
		if err := pool.QueryRow(ctx, `SELECT user_count FROM issues WHERE id = $1`, issues[i]).Scan(&n); err != nil {
			t.Fatal(err)
		}
		ident := store.UserIdent(ev.User)
		switch {
		case ident == "" && (len(hashes) != 0 || n != 0):
			t.Errorf("evento %d: %d usuarios y user_count %d; quiero ninguno", i, len(hashes), n)
		case ident != "" && (len(hashes) != 1 || !bytes.Equal(hashes[0], store.UserHash(key, ident)) || n != 1):
			t.Errorf("evento %d: %x, user_count %d; quiero el hash de %s", i, hashes, n, ident)
		}
	}
}
