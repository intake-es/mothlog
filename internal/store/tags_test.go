package store

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"strconv"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/intake-es/intake-errors/migrations"
)

// La migración cuenta las etiquetas y los usuarios de los eventos que ya
// había, con el mismo hash que la ingesta.
func TestMigrationTagsBackfill(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, testSchemaURL(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	db := stdlib.OpenDBFromPool(pool)
	t.Cleanup(func() { _ = db.Close() })
	p, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS,
		goose.WithSlog(slog.New(slog.NewTextHandler(io.Discard, nil))))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 9); err != nil {
		t.Fatal(err)
	}
	var project, issue, empty int64
	if err := pool.QueryRow(ctx, `INSERT INTO projects (name, public_key) VALUES ('web', 'clave') RETURNING id`).Scan(&project); err != nil {
		t.Fatal(err)
	}
	for _, id := range []*int64{&issue, &empty} {
		if err := pool.QueryRow(ctx, `
			INSERT INTO issues (project_id, fingerprint, title, first_seen, last_seen, event_count)
			VALUES ($1, gen_random_uuid()::text, 'TypeError', now(), now(), 4) RETURNING id`, project).Scan(id); err != nil {
			t.Fatal(err)
		}
	}
	for _, data := range []string{
		`{"level":"error","tags":{"org":"7"},"user":{"id":"42","email":"ana@x.es"},"contexts":{"browser":{"name":"Safari"},"os":{"name":"iOS"}}}`,
		`{"level":"error","tags":{"org":"7"},"user":{"email":" Ana@X.es "}}`,
		`{"level":"warning","tags":{"org":"9"},"user":{"email":"ana@x.es"}}`,
		`{"level":"error","user":{"id":"42"},"contexts":{"browser":{"name":"Safari"}}}`,
	} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO events (issue_id, project_id, event_id, occurred_at, release, environment, data)
			VALUES ($1, $2, gen_random_uuid(), now(), 'r1', 'production', $3)`, issue, project, data); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := p.UpTo(ctx, 11); err != nil {
		t.Fatal(err)
	}
	s := storeOn(pool)
	tags, err := s.IssueTags(ctx, issue)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, d := range tags.Tags {
		var b bytes.Buffer
		for _, v := range d.Values {
			b.WriteString(v.Value + "=" + strconv.FormatInt(v.Count, 10) + " ")
		}
		got[d.Key] = b.String()
	}
	want := map[string]string{
		"release":     "r1=4 ",
		"environment": "production=4 ",
		"level":       "error=3 warning=1 ",
		"org":         "7=2 9=1 ",
		"browser":     "Safari=2 ",
		"os":          "iOS=1 ",
	}
	if len(got) != len(want) {
		t.Errorf("claves = %v", got)
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s = %q; quiero %q", k, got[k], w)
		}
	}
	// id:42 y email:ana@x.es.
	if tags.UserCount != 2 {
		t.Errorf("usuarios = %d", tags.UserCount)
	}
	key, err := LoadUserHashKey(ctx, s.pool)
	if err != nil || len(key) != 32 {
		t.Fatalf("clave = %x, %v", key, err)
	}
	for _, ident := range []string{"id:42", "email:ana@x.es"} {
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM issue_users WHERE issue_id = $1 AND user_hash = $2`,
			issue, UserHash(key, ident)).Scan(&n); err != nil || n != 1 {
			t.Errorf("%s: %d, %v; el hash de SQL no es el de Go", ident, n, err)
		}
	}
	var n int64
	if err := pool.QueryRow(ctx, `SELECT user_count FROM issues WHERE id = $1`, empty).Scan(&n); err != nil || n != 0 {
		t.Errorf("issue sin eventos: %d, %v", n, err)
	}

	if _, err := p.DownTo(ctx, 9); err != nil {
		t.Fatalf("bajar: %v", err)
	}
	if _, err := p.Up(ctx); err != nil {
		t.Fatalf("volver a subir: %v", err)
	}
}
