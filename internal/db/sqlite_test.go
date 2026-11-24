package db

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestSQLitePath(t *testing.T) {
	for in, want := range map[string]string{
		"sqlite:///var/lib/mothlog/mothlog.db": "/var/lib/mothlog/mothlog.db",
		"sqlite://./mothlog.db":                "./mothlog.db",
		"sqlite://datos/mothlog.db?x=1":        "datos/mothlog.db",
		"sqlite:mothlog.db":                    "mothlog.db",
	} {
		if got, err := SQLitePath(in); err != nil || got != want {
			t.Errorf("%s: %q, %v; quiero %q", in, got, err, want)
		}
	}
	if _, err := SQLitePath("sqlite://"); err == nil {
		t.Error("sin ruta debería fallar")
	}
	if IsSQLiteURL("postgres://x@y/z") || !IsSQLiteURL("sqlite:///x.db") {
		t.Error("IsSQLiteURL")
	}
}

func TestSQLiteDialect(t *testing.T) {
	d := SQLiteDialect
	for _, c := range []struct{ got, want string }{
		{d.ForUpdate("m"), ""},
		{d.ForUpdateSkipLocked(), ""},
		{d.InList("o.channel", "$2"), "o.channel IN (SELECT value FROM json_each($2))"},
		{d.InBytes("u.user_hash", "$3"), "u.user_hash IN (SELECT unhex(value) FROM json_each($3))"},
		{d.Unnest("h", 6, "issue_id bigint", "user_hash bytea"),
			"(SELECT j.value AS issue_id, unhex(($7 ->> j.key)) AS user_hash FROM json_each($6) j) AS h"},
		{d.UnnestNumbered("e", "n", 1, "id bigint", "at timestamptz"),
			"(SELECT j.value AS id, ($2 ->> j.key) AS at, j.key + 1 AS n FROM json_each($1) j) AS e"},
		{d.Greatest("a", "$2"), "max(COALESCE(a, $2), COALESCE($2, a))"},
		{d.Least("a"), "min(a)"},
		{d.ILike("t.value", "$3"), `lower(t.value) LIKE lower($3) ESCAPE '\'`},
		{d.SecondsBetween("now()", "c"), "(CAST((now()) - (c) AS REAL) / 1000000)"},
		{d.AddMinutes("m.at", "m.margin"), "((m.at) + CAST(round((m.margin) * 60000000) AS INTEGER))"},
		{d.JSONType("conditions", "users"), "json_type(conditions, '$.users')"},
		{d.Typed("$1", "uuid"), "$1"},
		{d.Collate("p.name"), "p.name COLLATE en_us"},
	} {
		if c.got != c.want {
			t.Errorf("%q; quiero %q", c.got, c.want)
		}
	}
}

func openTest(t *testing.T, exclusive bool) (*SQLite, string) {
	t.Helper()
	url := "sqlite://" + filepath.Join(t.TempDir(), "prueba.db")
	s, err := OpenSQLite(context.Background(), url, SQLiteOptions{Exclusive: exclusive})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s, url
}

func TestSQLiteRefusesSecondInstance(t *testing.T) {
	s, url := openTest(t, true)
	_, err := OpenSQLite(context.Background(), url, SQLiteOptions{Exclusive: true})
	if !errors.Is(err, ErrLocked) || !strings.Contains(err.Error(), "only one instance") {
		t.Fatalf("segunda instancia: %v", err)
	}
	// Las órdenes de la línea de comandos no cogen el cerrojo.
	cmd, err := OpenSQLite(context.Background(), url, SQLiteOptions{})
	if err != nil {
		t.Fatalf("orden: %v", err)
	}
	cmd.Close()
	s.Close()
	again, err := OpenSQLite(context.Background(), url, SQLiteOptions{Exclusive: true})
	if err != nil {
		t.Fatalf("tras cerrar la primera: %v", err)
	}
	again.Close()
}

type doc struct {
	A string `json:"a"`
}

// Lo que va y vuelve de la base es lo mismo que con Postgres: fechas,
// listas, JSON, NULL y los errores de las restricciones.
func TestSQLiteRoundTrip(t *testing.T) {
	s, _ := openTest(t, false)
	ctx := context.Background()
	if _, err := s.Exec(ctx, `CREATE TABLE t (id INTEGER PRIMARY KEY, at INTEGER, at2 INTEGER, tags TEXT, d TEXT,
		raw TEXT, b BLOB, ok INTEGER, n INTEGER UNIQUE) STRICT`); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 23, 10, 11, 12, 123456000, time.UTC)
	if _, err := s.Exec(ctx, `INSERT INTO t VALUES (1, $1, $2, $3, $4, $5, $6, $7, 1)`,
		at, (*time.Time)(nil), []string{"a", "ñ"}, doc{A: "x"}, json.RawMessage(`{"k":1}`), []byte{0, 1}, true); err != nil {
		t.Fatal(err)
	}
	var (
		gotAt  time.Time
		gotAt2 *time.Time
		tags   []string
		d      doc
		raw    json.RawMessage
		b      []byte
		ok     bool
	)
	if err := s.QueryRow(ctx, `SELECT at, at2, tags, d, raw, b, ok FROM t`).Scan(&gotAt, &gotAt2, &tags, &d, &raw, &b, &ok); err != nil {
		t.Fatal(err)
	}
	if !gotAt.Equal(at) || gotAt2 != nil || !slices.Equal(tags, []string{"a", "ñ"}) || d.A != "x" ||
		string(raw) != `{"k":1}` || !slices.Equal(b, []byte{0, 1}) || !ok {
		t.Errorf("leído %v %v %v %v %s %v %v", gotAt, gotAt2, tags, d, raw, b, ok)
	}
	var n int
	if err := s.QueryRow(ctx, `SELECT count(*) FROM t WHERE `+SQLiteDialect.InList("id", "$1"), []int64{1, 2}).Scan(&n); err != nil || n != 1 {
		t.Errorf("InList = %d, %v", n, err)
	}
	if _, err := s.Exec(ctx, `INSERT INTO t (id, n) VALUES (2, 1)`); !IsUniqueViolation(err) {
		t.Errorf("repetido: %v", err)
	}
	var ilike bool
	if err := s.QueryRow(ctx, `SELECT `+SQLiteDialect.ILike("'ÑANDÚ'", "$1"), "%ñan%").Scan(&ilike); err != nil || !ilike {
		t.Errorf("ILike con Unicode = %v, %v", ilike, err)
	}
	if err := s.QueryRow(ctx, `SELECT id FROM t WHERE id = 99`).Scan(&n); !errors.Is(err, ErrNoRows) {
		t.Errorf("sin filas: %v", err)
	}
}

// Como en Postgres, un error deja la transacción abortada hasta volver a un savepoint.
func TestSQLiteTxAbortsLikePostgres(t *testing.T) {
	s, _ := openTest(t, false)
	ctx := context.Background()
	if _, err := s.Exec(ctx, `CREATE TABLE t (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	tx, err := s.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sp, err := tx.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sp.Exec(ctx, `INSERT INTO t VALUES (1), (1)`); !IsUniqueViolation(err) {
		t.Fatalf("repetido: %v", err)
	}
	if _, err := sp.Exec(ctx, `INSERT INTO t VALUES (2)`); err == nil {
		t.Fatal("tras el error, la transacción debería estar abortada")
	}
	if err := sp.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO t VALUES (3)`); err != nil {
		t.Fatalf("tras volver al savepoint: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO nada VALUES (1)`); err == nil {
		t.Fatal("tabla que no existe")
	}
	if err := tx.Commit(ctx); err == nil {
		t.Fatal("el commit de una transacción abortada debería fallar")
	}
	var n int
	if err := s.QueryRow(ctx, `SELECT count(*) FROM t`).Scan(&n); err != nil || n != 0 {
		t.Errorf("filas = %d, %v; el commit fallido no deja nada", n, err)
	}
}

// El orden de en_us es el que da Postgres 16 con en_US.UTF-8 (ORDER BY s).
func TestCollateLikePostgres(t *testing.T) {
	want := []string{"01", "1", "10", "10.0.0.1", "2.0.0", "9", "@a", "a b", "a-b", "ab", "ana@example.com",
		"  Ana@Example.com ", "angel", "Ángel", "api", "Api", "API", "{{auto}}", "#b", "browser", "browser.name", ".c",
		"(d)", "eclair", "Eclair", "éclair", "fixture-release-1", "fixture-release-10", "fixture-release-2",
		"Importada@Example.com", "luis", "ma", "m-b", "m_b", "mb", "nandu", "ñandú", "Ñu", "org-0", "org-1", "org-10",
		"org-2", "os.name", "os_name", "osname", "production", "release", "sentry:x", "staging", "u-1", "u_luis", "web",
		"web-1", "web_1", "web1", "weba", "-x", "_x", "x", "X", "z", "zoe", "Zoe"}
	got := slices.Clone(want)
	slices.Reverse(got)
	slices.SortStableFunc(got, compareEnUS)
	if !slices.Equal(got, want) {
		t.Errorf("orden:\n%q\nquiero\n%q", got, want)
	}
}
