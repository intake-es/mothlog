package webauth

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/intake-es/intake-errors/internal/api"
	"github.com/intake-es/intake-errors/internal/store"
	"github.com/intake-es/intake-errors/internal/store/storetest"
)

// realHarness es el login con la base de verdad y las rutas de verdad de
// /api/0/ y /mcp, como las monta main.
func realHarness(t *testing.T, cfg Config) (*harness, *storetest.DB) {
	t.Helper()
	db := storetest.NewDB(t)
	h := newHarness(t, cfg)
	h.auth.SavePeople(db.Store)
	mux := http.NewServeMux()
	h.auth.Register(mux)
	if err := api.Register(mux, db.Store, api.Options{PublicURL: cfg.PublicURL,
		Auth: api.RequireAuth(api.BearerToken(db.Store), h.auth.Authenticator())}); err != nil {
		t.Fatal(err)
	}
	h.mux = mux
	return h, db
}

const mcpInitialize = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"0"}}}`

// mcp manda un initialize a /mcp con la cookie y devuelve el código.
func (h *harness) mcp(t *testing.T, c *http.Cookie) *http.Response {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(mcpInitialize))
	if h.host != "" {
		req.Host = h.host
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Origin", publicURL)
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	h.mux.ServeHTTP(rec, req)
	return rec.Result()
}

// later avanza el reloj: cada paso pasa en otro momento.
func (h *harness) later() { h.clock.now = h.clock.now.Add(time.Second) }

func TestSessionRevokedOnServer(t *testing.T) {
	gh := newFakeGitHub(t)
	h, db := realHarness(t, githubConfig(gh))
	ctx := context.Background()

	works := func(name string, c *http.Cookie, want int) {
		t.Helper()
		res := h.do(t, http.MethodGet, "/api/0/issues/", []*http.Cookie{c}, nil)
		if res.StatusCode != want {
			t.Errorf("%s: /api/0/issues/ = %d; quiero %d", name, res.StatusCode, want)
		}
		if cc := res.Header.Get("Cache-Control"); cc != "no-store" {
			t.Errorf("%s: /api/0/issues/ con Cache-Control %q", name, cc)
		}
		mres := h.mcp(t, c)
		if mres.StatusCode != want {
			b, _ := io.ReadAll(mres.Body)
			t.Errorf("%s: /mcp = %d %s; quiero %d", name, mres.StatusCode, b, want)
		}
		if cc := mres.Header.Get("Cache-Control"); cc != "no-store" {
			t.Errorf("%s: /mcp con Cache-Control %q", name, cc)
		}
	}

	before := cookieNamed(h.login(t, "ana", "/"), sessionCookie)
	if before == nil || before.Name != "__Host-ie_session" {
		t.Fatalf("cookie = %+v", before)
	}
	works("recién entrada", before, http.StatusOK)

	// Cerrar sesión la revoca en el servidor, no solo en el navegador.
	h.later()
	res := h.do(t, http.MethodPost, "/auth/logout", []*http.Cookie{before}, map[string]string{"Origin": publicURL})
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("logout = %d", res.StatusCode)
	}
	works("la de antes de cerrar sesión", before, http.StatusUnauthorized)

	h.later()
	fresh := cookieNamed(h.login(t, "ana", "/"), sessionCookie)
	works("una sesión nueva", fresh, http.StatusOK)

	// people revoke: la baja de alguien.
	h.later()
	if login, err := db.RevokeSessions(ctx, "ANA", h.clock.now); err != nil || login != "ana" {
		t.Fatalf("revocar = %q, %v", login, err)
	}
	works("la de antes de people revoke", fresh, http.StatusUnauthorized)
	h.later()
	works("otra nueva", cookieNamed(h.login(t, "ana", "/"), sessionCookie), http.StatusOK)

	// Una cookie bien firmada de alguien que no está en people no vale.
	ghost, _ := h.auth.sign("session", Session{Login: "nadie", IssuedAt: h.clock.now.UnixMicro(), Expires: h.clock.now.Add(time.Hour).Unix()})
	works("de alguien que no está", &http.Cookie{Name: h.auth.sessionName(), Value: ghost}, http.StatusUnauthorized)
}

func TestGitHubIdentityByID(t *testing.T) {
	gh := newFakeGitHub(t)
	h, db := realHarness(t, githubConfig(gh))
	ctx := context.Background()
	p := db.AddProject(t, "web")
	issue := db.AddIssue(t, storetest.Issue{ProjectID: p.ID, Title: "TypeError"})
	base := "/api/0/issues/" + strconv.FormatInt(issue, 10)
	origin := map[string]string{"Origin": publicURL}

	whoami := func(c *http.Cookie) string {
		t.Helper()
		res := h.do(t, http.MethodGet, "/auth/session", []*http.Cookie{c}, nil)
		var body sessionJSON
		_ = json.NewDecoder(res.Body).Decode(&body)
		if body.User == nil {
			return ""
		}
		return body.User.Login
	}
	comment := func(c *http.Cookie) int64 {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, base+"/comments/", strings.NewReader(`{"body":"lo miro yo"}`))
		req.Header.Set("Origin", publicURL)
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(c)
		rec := httptest.NewRecorder()
		h.mux.ServeHTTP(rec, req)
		var out struct{ ID int64 }
		if rec.Code != http.StatusCreated || json.Unmarshal(rec.Body.Bytes(), &out) != nil {
			t.Fatalf("comentar = %d %s", rec.Code, rec.Body)
		}
		return out.ID
	}

	ana := cookieNamed(h.login(t, "ana", "/"), sessionCookie)
	if got := whoami(ana); got != "ana" {
		t.Fatalf("ana = %q", got)
	}
	c1 := comment(ana)

	// Ana se cambia el login en GitHub: con el mismo id, sigue siendo ana.
	gh.account("ana-renombrada", 101, "ana-nueva")
	renamed := cookieNamed(h.login(t, "ana-renombrada", "/"), sessionCookie)
	if got := whoami(renamed); got != "ana" {
		t.Errorf("la cuenta renombrada entra como %q; quiero ana", got)
	}

	// Otra cuenta coge el login que dejó libre: es otra persona.
	gh.account("impostora", 202, "ana")
	other := cookieNamed(h.login(t, "impostora", "/"), sessionCookie)
	if got := whoami(other); got != "ana~2" {
		t.Fatalf("la otra cuenta con el login ana entra como %q; quiero ana~2", got)
	}
	res := h.do(t, http.MethodDelete, base+"/comments/"+strconv.FormatInt(c1, 10), []*http.Cookie{other}, origin)
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("ana~2 borra el comentario de ana = %d", res.StatusCode)
	}
	res = h.do(t, http.MethodDelete, base+"/comments/"+strconv.FormatInt(c1, 10), []*http.Cookie{renamed}, origin)
	if res.StatusCode != http.StatusNoContent {
		t.Errorf("ana, renombrada, borra su comentario = %d", res.StatusCode)
	}

	// Una fila de antes, guardada por login, pasa al id la primera vez que
	// entra una cuenta con ese login; la siguiente con otro id es otra persona.
	if _, err := db.Pool().Exec(ctx, `INSERT INTO people (login, name, provider, subject) VALUES ('luis', 'Luis', 'github', 'login:luis')`); err != nil {
		t.Fatal(err)
	}
	gh.account("luis", 303, "Luis")
	if got := whoami(cookieNamed(h.login(t, "luis", "/"), sessionCookie)); got != "luis" {
		t.Errorf("luis, de antes, entra como %q", got)
	}
	gh.account("luis-otro", 404, "luis")
	if got := whoami(cookieNamed(h.login(t, "luis-otro", "/"), sessionCookie)); got != "luis~2" {
		t.Errorf("otra cuenta con el login luis entra como %q; quiero luis~2", got)
	}
	people, err := db.People(ctx)
	if err != nil {
		t.Fatal(err)
	}
	subjects := map[string]string{}
	for _, p := range people {
		subjects[p.Login] = p.Subject
	}
	want := map[string]string{"ana": "101", "ana~2": "202", "luis": "303", "luis~2": "404"}
	for login, sub := range want {
		if subjects[login] != sub {
			t.Errorf("subject de %s = %q; quiero %q (%v)", login, subjects[login], sub, subjects)
		}
	}

	// Sin id no se entra.
	if res := h.login(t, "sinid", "/"); res.Header.Get("Location") != "/login?error=github" || cookieNamed(res, sessionCookie) != nil {
		t.Errorf("sin id = %s", res.Header.Get("Location"))
	}
}

func TestOIDCCannotTakeAnotherName(t *testing.T) {
	f := newFakeOIDC(t)
	h, db := realHarness(t, oidcConfig(f))
	h.auth.oidc.cfg.AllowedDomains = nil
	h.auth.oidc.cfg.AllowAny = true
	ctx := context.Background()
	whoami := func(res *http.Response) string {
		t.Helper()
		c := cookieNamed(res, sessionCookie)
		if c == nil {
			t.Fatalf("sin sesión: %s", res.Header.Get("Location"))
		}
		r := h.do(t, http.MethodGet, "/auth/session", []*http.Cookie{c}, nil)
		var body sessionJSON
		_ = json.NewDecoder(r.Body).Decode(&body)
		return body.User.Login
	}

	// Llega primero quien pone el correo de jefa como preferred_username, y
	// otra con el login de GitHub de ana.
	if got := whoami(h.oidcLogin(t, f, fakeGrant{claims: map[string]any{"sub": "u-eve", "preferred_username": "jefa@example.com"}})); got != "oidc:jefa@example.com" {
		t.Errorf("preferred_username con el correo de otra = %q", got)
	}
	if got := whoami(h.oidcLogin(t, f, fakeGrant{claims: map[string]any{"sub": "u-mal", "preferred_username": "ana", "email": "ana@example.com", "email_verified": false}})); got != "oidc:ana" {
		t.Errorf("preferred_username con el login de otra = %q", got)
	}
	// La de verdad entra después con su correo verificado y se lo queda.
	if got := whoami(h.oidcLogin(t, f, fakeGrant{claims: user("u-jefa", "jefa@example.com")})); got != "jefa@example.com" {
		t.Errorf("jefa = %q", got)
	}
	if _, err := db.SaveIdentity(ctx, store.Person{Login: "ana", Provider: store.LoginGitHub, Subject: "101"}); err != nil {
		t.Fatal(err)
	}
	people, _ := db.People(ctx)
	var logins []string
	for _, p := range people {
		logins = append(logins, p.Login)
	}
	slices.Sort(logins)
	if strings.Join(logins, ",") != "ana,jefa@example.com,oidc:ana,oidc:jefa@example.com" {
		t.Errorf("personas = %v", logins)
	}
}

// Antes del prefijo, alguien de OIDC sin correo verificado pudo quedarse con
// un correo ajeno. Al volver a entrar, su login pasa a oidc: con lo suyo.
func TestOIDCLegacyLoginGetsPrefix(t *testing.T) {
	f := newFakeOIDC(t)
	h, db := realHarness(t, oidcConfig(f))
	h.auth.oidc.cfg.AllowedDomains = nil
	h.auth.oidc.cfg.AllowAny = true
	ctx := context.Background()
	p := db.AddProject(t, "web")
	issue := db.AddIssue(t, storetest.Issue{ProjectID: p.ID, Title: "TypeError"})
	if _, err := db.Pool().Exec(ctx, `INSERT INTO people (login, name, provider, subject) VALUES ('jefa@example.com', 'Eve', 'oidc', 'u-eve')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool().Exec(ctx, `UPDATE issues SET assignee = 'jefa@example.com', assignee_kind = 'user', assigned_at = now() WHERE id = $1`, issue); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool().Exec(ctx, `INSERT INTO issue_comments (issue_id, author, author_kind, body) VALUES ($1, 'jefa@example.com', 'user', 'mío')`, issue); err != nil {
		t.Fatal(err)
	}
	// Una sesión firmada con el login de antes deja de valer al renombrarla.
	old, _ := h.auth.sign("session", Session{Login: "jefa@example.com", IssuedAt: h.clock.now.UnixMicro(), Expires: h.clock.now.Add(time.Hour).Unix()})
	oldCookie := &http.Cookie{Name: h.auth.sessionName(), Value: old}
	h.later()
	if res := h.do(t, http.MethodGet, "/api/0/issues/", []*http.Cookie{oldCookie}, nil); res.StatusCode != http.StatusOK {
		t.Fatalf("la sesión de antes, antes de renombrar = %d", res.StatusCode)
	}

	res := h.oidcLogin(t, f, fakeGrant{claims: map[string]any{"sub": "u-eve", "preferred_username": "jefa@example.com"}})
	c := cookieNamed(res, sessionCookie)
	if c == nil {
		t.Fatalf("sin sesión: %s", res.Header.Get("Location"))
	}
	r := h.do(t, http.MethodGet, "/auth/session", []*http.Cookie{c}, nil)
	var body sessionJSON
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.User == nil || body.User.Login != "oidc:jefa@example.com" {
		t.Fatalf("al volver entra como %+v", body.User)
	}
	var assignee, author string
	if err := db.Pool().QueryRow(ctx, `SELECT assignee FROM issues WHERE id = $1`, issue).Scan(&assignee); err != nil || assignee != "oidc:jefa@example.com" {
		t.Errorf("asignado = %q, %v", assignee, err)
	}
	if err := db.Pool().QueryRow(ctx, `SELECT author FROM issue_comments`).Scan(&author); err != nil || author != "oidc:jefa@example.com" {
		t.Errorf("autor = %q, %v", author, err)
	}
	if res := h.do(t, http.MethodGet, "/api/0/issues/", []*http.Cookie{oldCookie}, nil); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("la sesión con el login de antes = %d", res.StatusCode)
	}
	// El correo queda libre para quien lo tiene verificado.
	res = h.oidcLogin(t, f, fakeGrant{claims: user("u-jefa", "jefa@example.com")})
	r = h.do(t, http.MethodGet, "/auth/session", []*http.Cookie{cookieNamed(res, sessionCookie)}, nil)
	body = sessionJSON{}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.User == nil || body.User.Login != "jefa@example.com" {
		t.Errorf("la dueña del correo entra como %+v", body.User)
	}
}

func TestDevLoginRealStore(t *testing.T) {
	h, db := realHarness(t, Config{ListenAddr: "127.0.0.1:8080", DevLogin: true})
	ctx := context.Background()
	if res := h.do(t, http.MethodGet, "/auth/dev/login", nil, nil); cookieNamed(res, sessionCookie) != nil {
		t.Fatal("sin clave hay sesión")
	}
	s := cookieNamed(h.devLogin(t, "/"), sessionCookie)
	res := h.do(t, http.MethodGet, "/api/0/issues/", []*http.Cookie{s}, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("con la sesión de dev = %d", res.StatusCode)
	}
	res = h.do(t, http.MethodGet, "/api/0/issues/", []*http.Cookie{s}, map[string]string{"Host": "rebind.example:8080"})
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("con un Host ajeno = %d", res.StatusCode)
	}
	h.later()
	if _, err := db.RevokeSessions(ctx, "dev", h.clock.now); err != nil {
		t.Fatal(err)
	}
	if res := h.do(t, http.MethodGet, "/api/0/issues/", []*http.Cookie{s}, nil); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("revocada = %d", res.StatusCode)
	}
}
