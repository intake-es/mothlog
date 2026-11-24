package store

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/intake-es/mothlog/internal/db"
	"github.com/intake-es/mothlog/migrations"
	sqlitemigrations "github.com/intake-es/mothlog/migrations/sqlite"
)

func TestOAuthGrantFollowsPerson(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)
	if _, err := s.SaveIdentity(ctx, Person{Login: "ana", Provider: LoginDev, Subject: "ana"}); err != nil {
		t.Fatal(err)
	}
	ttl := OAuthTTL{Access: time.Hour, Refresh: 24 * time.Hour}
	connect := func(id, login string) OAuthTokens {
		t.Helper()
		if err := s.CreateOAuthRequest(ctx, OAuthRequest{ID: id, ClientID: "c", ClientName: "Cliente", RedirectURI: "https://c.example.com/cb",
			Scopes: []string{"read"}, CodeChallenge: "x", Resource: "https://e.example.com/mcp", ExpiresAt: now.Add(time.Minute)}, now); err != nil {
			t.Fatal(err)
		}
		_, code, err := s.ApproveOAuthRequest(ctx, id, login, []string{"read"}, now.Add(time.Minute), now)
		if err != nil {
			t.Fatal(err)
		}
		tok, err := s.ExchangeOAuthCode(ctx, code, func(OAuthRequest) error { return nil }, ttl, now)
		if err != nil {
			t.Fatal(err)
		}
		return tok
	}
	tok := connect("r1", "ana")

	// Renombrar a la persona no le quita lo que ha conectado.
	login, err := s.RenamePerson(ctx, "ana", "ana-nueva")
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.AuthenticateOAuth(ctx, tok.Access, "198.51.100.7", now)
	if err != nil || a.Login != login || a.ClientName != "Cliente" {
		t.Fatalf("tras renombrar = %+v, %v", a, err)
	}
	gs, err := s.OAuthGrants(ctx, login, now)
	if err != nil || len(gs) != 1 || gs[0].LastUsedIP == nil || *gs[0].LastUsedIP != "198.51.100.7" {
		t.Fatalf("concesiones = %+v, %v", gs, err)
	}

	// people revoke: caen todas, sin mirar mayúsculas.
	connect("r2", login)
	n, err := s.RevokeOAuthGrantsOf(ctx, "ANA-NUEVA", now)
	if err != nil || n != 2 {
		t.Fatalf("revocadas = %d, %v", n, err)
	}
	if _, err := s.AuthenticateOAuth(ctx, tok.Access, "198.51.100.7", now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("tras revocar = %v", err)
	}
	if _, err := s.RefreshOAuth(ctx, tok.Refresh, "c", nil, ttl, now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("refresco tras revocar = %v", err)
	}
	if err := s.PruneOAuth(ctx, now.Add(40*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
}

func TestHistoryVia(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	p, err := s.CreateProject(ctx, "p", "clave")
	if err != nil {
		t.Fatal(err)
	}
	var issue int64
	if err := s.pool.QueryRow(ctx, `INSERT INTO issues (project_id, fingerprint, title, first_seen, last_seen) VALUES ($1, 'f', 't', now(), now()) RETURNING id`, p.ID).Scan(&issue); err != nil {
		t.Fatal(err)
	}
	via := Actor{Name: "ana", Kind: ActorUser, Via: "Editor"}
	if _, err := s.AddComment(ctx, issue, via, "hola"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ChangeIssueStatus(ctx, issue, "resolved", StatusChange{Actor: &via, Source: SourceMCP, Comment: "hecho"}); err != nil {
		t.Fatal(err)
	}
	h, err := s.History(ctx, issue, &Actor{Name: "ana", Kind: ActorUser})
	if err != nil {
		t.Fatal(err)
	}
	if len(h) != 2 || *h[0].Author != via || !h[0].Mine || *h[1].Author != via || h[1].Comment.Author != via || !h[1].Comment.Mine {
		t.Fatalf("historial = %+v", h)
	}
}

// La 00026 sube sobre datos de antes, con los mismos comentarios y sin
// autor_via, y baja sin dejar nada suyo.
func checkOAuthMigration(t *testing.T, ctx context.Context, q db.Querier, up, down func(version int64) error) {
	t.Helper()
	if err := up(24); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Exec(ctx, `INSERT INTO projects (name, public_key) VALUES ('web', 'clave-web')`); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Exec(ctx, `
		INSERT INTO issues (project_id, fingerprint, title, first_seen, last_seen, event_count)
		SELECT id, 'fp', 'TypeError', now(), now(), 1 FROM projects`); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Exec(ctx, `INSERT INTO people (login, name, provider, subject) VALUES ('ana', 'Ana', 'dev', 'ana')`); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Exec(ctx, `INSERT INTO issue_comments (issue_id, author, author_kind, body) SELECT id, 'ana', 'user', 'antes' FROM issues`); err != nil {
		t.Fatal(err)
	}
	if err := up(26); err != nil {
		t.Fatal(err)
	}
	var via *string
	if err := q.QueryRow(ctx, `SELECT author_via FROM issue_comments`).Scan(&via); err != nil || via != nil {
		t.Fatalf("author_via de un comentario de antes = %v, %v", via, err)
	}
	if _, err := q.Exec(ctx, `
		INSERT INTO oauth_grants (login, client_id, client_name, redirect_uri, scopes, resource, created_at)
		VALUES ('ana', 'c', 'Cliente', 'https://c.example.com/cb', 'read', 'https://e.example.com/mcp', now())`); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Exec(ctx, `UPDATE people SET login = 'ana~2' WHERE login = 'ana'`); err != nil {
		t.Fatal(err)
	}
	var login string
	if err := q.QueryRow(ctx, `SELECT login FROM oauth_grants`).Scan(&login); err != nil || login != "ana~2" {
		t.Fatalf("la concesión no sigue a la persona: %q, %v", login, err)
	}
	if _, err := q.Exec(ctx, `
		INSERT INTO oauth_grants (login, client_id, client_name, redirect_uri, scopes, resource, created_at)
		VALUES ('nadie', 'c', 'Cliente', 'https://c.example.com/cb', 'read', 'https://e.example.com/mcp', now())`); err == nil {
		t.Error("una concesión de alguien que no existe no debería caber")
	}
	if err := down(24); err != nil {
		t.Fatalf("bajar: %v", err)
	}
	if _, err := q.Exec(ctx, `SELECT author_via FROM issue_comments`); err == nil {
		t.Error("tras bajar sigue author_via")
	}
	if err := up(26); err != nil {
		t.Fatalf("volver a subir: %v", err)
	}
}

func TestMigrationOAuthPostgres(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, testSchemaURL(t, migrationsOnlyPostgres))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	sqlDB := stdlib.OpenDBFromPool(pool)
	t.Cleanup(func() { _ = sqlDB.Close() })
	p, err := goose.NewProvider(goose.DialectPostgres, sqlDB, migrations.FS,
		goose.WithSlog(slog.New(slog.NewTextHandler(io.Discard, nil))), goose.WithAllowOutofOrder(true))
	if err != nil {
		t.Fatal(err)
	}
	checkOAuthMigration(t, ctx, db.NewPostgres(pool),
		func(v int64) error { _, err := p.UpTo(ctx, v); return err },
		func(v int64) error { _, err := p.DownTo(ctx, v); return err })
}

func TestMigrationOAuthSQLite(t *testing.T) {
	ctx := context.Background()
	lite, err := db.OpenSQLite(ctx, "sqlite://"+filepath.Join(t.TempDir(), "mothlog.db"), db.SQLiteOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(lite.Close)
	p, err := goose.NewProvider(goose.DialectSQLite3, lite.WriteDB(), sqlitemigrations.FS,
		goose.WithSlog(slog.New(slog.NewTextHandler(io.Discard, nil))), goose.WithAllowOutofOrder(true))
	if err != nil {
		t.Fatal(err)
	}
	checkOAuthMigration(t, ctx, lite,
		func(v int64) error { _, err := p.UpTo(ctx, v); return err },
		func(v int64) error { _, err := p.DownTo(ctx, v); return err })
}
