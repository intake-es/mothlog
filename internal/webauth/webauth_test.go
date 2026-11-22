package webauth

import (
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/intake-es/intake-errors/internal/api"
)

const (
	testSecret = "0123456789abcdef0123456789abcdef"
	publicURL  = "https://errors.example.com"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// fakeGitHub hace de github.com y de api.github.com: cada código es un usuario.
type fakeGitHub struct {
	*httptest.Server
	mu        sync.Mutex
	exchanges []url.Values
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	t.Helper()
	gh := &fakeGitHub{}
	members := map[string]struct {
		code  int
		state string
	}{
		"ana":  {http.StatusOK, "active"},
		"pepe": {http.StatusOK, "pending"},
		"eve":  {http.StatusNotFound, ""},
		"rota": {http.StatusInternalServerError, ""},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /login/oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		gh.mu.Lock()
		gh.exchanges = append(gh.exchanges, r.PostForm)
		gh.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		code := r.PostForm.Get("code")
		if r.PostForm.Get("client_secret") != "secreto" || code == "caducado" {
			_, _ = w.Write([]byte(`{"error":"bad_verification_code"}`))
			return
		}
		_, _ = w.Write([]byte(`{"access_token":"tok-` + code + `","token_type":"bearer","scope":"read:org"}`))
	})
	user := func(r *http.Request) string {
		return strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer tok-")
	}
	mux.HandleFunc("GET /user", func(w http.ResponseWriter, r *http.Request) {
		login := user(r)
		if _, ok := members[login]; !ok {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"login": login, "name": strings.ToUpper(login[:1]) + login[1:], "id": 1,
			"avatar_url": "https://avatars.example.com/" + login})
	})
	mux.HandleFunc("GET /user/memberships/orgs/{org}", func(w http.ResponseWriter, r *http.Request) {
		m := members[user(r)]
		if r.PathValue("org") != "example-org" || m.code != http.StatusOK {
			code := m.code
			if code == 0 || r.PathValue("org") != "example-org" {
				code = http.StatusNotFound
			}
			w.WriteHeader(code)
			_, _ = w.Write([]byte(`{"message":"Not Found"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"state": m.state, "role": "member", "organization": map[string]any{"login": "example-org"}})
	})
	gh.Server = httptest.NewServer(mux)
	t.Cleanup(gh.Close)
	return gh
}

type clock struct{ now time.Time }

func (c *clock) Now() time.Time { return c.now }

type harness struct {
	auth  *Auth
	mux   *http.ServeMux
	clock *clock
}

func newHarness(t *testing.T, cfg Config) *harness {
	t.Helper()
	c := &clock{now: time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)}
	cfg.Now = c.Now
	a, err := New(cfg, quiet)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	a.Register(mux)
	protected := api.RequireAuth(a.Authenticator())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, _ := api.PrincipalFrom(r.Context())
		_, _ = w.Write([]byte(p.Kind + ":" + p.Name))
	}))
	mux.Handle("/api/0/", protected)
	return &harness{auth: a, mux: mux, clock: c}
}

func githubConfig(gh *fakeGitHub) Config {
	return Config{
		ClientID:      "cliente",
		ClientSecret:  "secreto",
		Org:           "example-org",
		SessionSecret: testSecret,
		PublicURL:     publicURL,
		ListenAddr:    ":8080",
		GitHubURL:     gh.URL,
		GitHubAPIURL:  gh.URL,
	}
}

func (h *harness) do(t *testing.T, method, target string, cookies []*http.Cookie, header map[string]string) *http.Response {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.mux.ServeHTTP(rec, req)
	return rec.Result()
}

func cookieNamed(res *http.Response, name string) *http.Cookie {
	for _, c := range res.Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// login hace el viaje entero: /auth/github/login, GitHub vuelve con code y
// state, y el callback. Devuelve la respuesta del callback.
func (h *harness) login(t *testing.T, code, next string) *http.Response {
	t.Helper()
	res := h.do(t, http.MethodGet, "/auth/github/login?next="+url.QueryEscape(next), nil, nil)
	if res.StatusCode != http.StatusFound {
		t.Fatalf("login = %d", res.StatusCode)
	}
	loc, _ := url.Parse(res.Header.Get("Location"))
	state := loc.Query().Get("state")
	sc := cookieNamed(res, stateCookie)
	if state == "" || sc == nil {
		t.Fatalf("sin state: %s %v", loc, res.Cookies())
	}
	return h.do(t, http.MethodGet, "/auth/github/callback?code="+code+"&state="+state, []*http.Cookie{sc}, nil)
}

func TestGitHubLogin(t *testing.T) {
	gh := newFakeGitHub(t)
	h := newHarness(t, githubConfig(gh))

	res := h.do(t, http.MethodGet, "/auth/github/login?next=/issues/42", nil, nil)
	loc, _ := url.Parse(res.Header.Get("Location"))
	q := loc.Query()
	if res.StatusCode != http.StatusFound || !strings.HasPrefix(loc.String(), gh.URL+"/login/oauth/authorize?") ||
		q.Get("client_id") != "cliente" || q.Get("scope") != "read:org" || len(q.Get("state")) != 64 ||
		q.Get("redirect_uri") != publicURL+"/auth/github/callback" {
		t.Fatalf("redirección a GitHub = %d %s", res.StatusCode, loc)
	}
	sc := cookieNamed(res, stateCookie)
	if sc == nil || !sc.HttpOnly || !sc.Secure || sc.SameSite != http.SameSiteLaxMode || sc.Path != "/auth/github/" || sc.MaxAge != 600 {
		t.Errorf("cookie de state = %+v", sc)
	}

	res = h.do(t, http.MethodGet, "/auth/github/callback?code=ana&state="+q.Get("state"), []*http.Cookie{sc}, nil)
	if res.StatusCode != http.StatusFound || res.Header.Get("Location") != "/issues/42" {
		t.Fatalf("callback = %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	if gone := cookieNamed(res, stateCookie); gone == nil || gone.MaxAge >= 0 {
		t.Errorf("la cookie de state no se borra: %+v", gone)
	}
	s := cookieNamed(res, sessionCookie)
	if s == nil || !s.HttpOnly || !s.Secure || s.SameSite != http.SameSiteLaxMode || s.Path != "/" || s.MaxAge != 7*24*3600 {
		t.Fatalf("cookie de sesión = %+v", s)
	}
	gh.mu.Lock()
	ex := gh.exchanges[len(gh.exchanges)-1]
	gh.mu.Unlock()
	if ex.Get("code") != "ana" || ex.Get("client_id") != "cliente" || ex.Get("redirect_uri") != publicURL+"/auth/github/callback" {
		t.Errorf("canje = %v", ex)
	}

	res = h.do(t, http.MethodGet, "/api/0/issues/", []*http.Cookie{s}, nil)
	if b, _ := io.ReadAll(res.Body); res.StatusCode != http.StatusOK || string(b) != "session:ana" {
		t.Errorf("con la sesión = %d %s", res.StatusCode, b)
	}

	res = h.do(t, http.MethodGet, "/auth/session", []*http.Cookie{s}, nil)
	var body sessionJSON
	_ = json.NewDecoder(res.Body).Decode(&body)
	if body.User == nil || body.User.Login != "ana" || body.User.Name != "Ana" || body.Org != "example-org" || len(body.Methods) != 1 || body.Methods[0] != "github" {
		t.Errorf("/auth/session = %+v", body)
	}
}

func TestGitHubLoginRejected(t *testing.T) {
	gh := newFakeGitHub(t)
	h := newHarness(t, githubConfig(gh))
	for _, tc := range []struct{ code, reason string }{
		{"eve", "org"},
		{"pepe", "org"},
		{"rota", "github"},
		{"caducado", "github"},
		{"desconocido", "github"},
	} {
		res := h.login(t, tc.code, "/")
		if res.StatusCode != http.StatusFound || res.Header.Get("Location") != "/login?error="+tc.reason {
			t.Errorf("%s = %d %s", tc.code, res.StatusCode, res.Header.Get("Location"))
		}
		if c := cookieNamed(res, sessionCookie); c != nil {
			t.Errorf("%s: dejó sesión %+v", tc.code, c)
		}
	}
}

func TestGitHubBadState(t *testing.T) {
	gh := newFakeGitHub(t)
	h := newHarness(t, githubConfig(gh))
	start := func() (string, *http.Cookie) {
		res := h.do(t, http.MethodGet, "/auth/github/login", nil, nil)
		loc, _ := url.Parse(res.Header.Get("Location"))
		return loc.Query().Get("state"), cookieNamed(res, stateCookie)
	}
	state, sc := start()
	other, _ := start()
	forged := *sc
	payload, _, _ := strings.Cut(sc.Value, ".")
	forged.Value = payload + ".firmafalsa"

	for _, tc := range []struct {
		name   string
		query  string
		cookie *http.Cookie
		later  time.Duration
	}{
		{"sin cookie", "code=ana&state=" + state, nil, 0},
		{"state de otro login", "code=ana&state=" + other, sc, 0},
		{"sin state", "code=ana", sc, 0},
		{"cookie con firma mala", "code=ana&state=" + state, &forged, 0},
		{"caducado", "code=ana&state=" + state, sc, 11 * time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h.clock.now = time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC).Add(tc.later)
			var cookies []*http.Cookie
			if tc.cookie != nil {
				cookies = append(cookies, tc.cookie)
			}
			res := h.do(t, http.MethodGet, "/auth/github/callback?"+tc.query, cookies, nil)
			if res.StatusCode != http.StatusFound || res.Header.Get("Location") != "/login?error=state" || cookieNamed(res, sessionCookie) != nil {
				t.Errorf("= %d %s", res.StatusCode, res.Header.Get("Location"))
			}
		})
	}
	h.clock.now = time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	gh.mu.Lock()
	n := len(gh.exchanges)
	gh.mu.Unlock()
	if n != 0 {
		t.Errorf("con un state malo se canjeó el código %d veces", n)
	}

	// El usuario cancela en GitHub: state bueno, sin code.
	res := h.do(t, http.MethodGet, "/auth/github/callback?error=access_denied&state="+state, []*http.Cookie{sc}, nil)
	if res.Header.Get("Location") != "/login?error=denied" {
		t.Errorf("cancelado = %s", res.Header.Get("Location"))
	}
}

func TestSessionCookie(t *testing.T) {
	gh := newFakeGitHub(t)
	h := newHarness(t, githubConfig(gh))
	good := cookieNamed(h.login(t, "ana", "/"), sessionCookie)
	payload, sig, _ := strings.Cut(good.Value, ".")

	other := githubConfig(gh)
	other.SessionSecret = strings.Repeat("x", 32)
	otherAuth, err := New(other, quiet)
	if err != nil {
		t.Fatal(err)
	}
	otherAuth.cfg.Now = h.clock.Now
	foreign, _ := otherAuth.sign("session", Session{Login: "ana", Expires: h.clock.now.Add(time.Hour).Unix()})
	admin, _ := h.auth.sign("session", Session{Login: "admin", Expires: h.clock.now.Add(time.Hour).Unix()})
	adminPayload, _, _ := strings.Cut(admin, ".")
	asState, _ := h.auth.sign("state", Session{Login: "ana", Expires: h.clock.now.Add(time.Hour).Unix()})

	for _, tc := range []struct {
		name, value string
		later       time.Duration
	}{
		{"firma mala", payload + ".AAAA" + sig[4:], 0},
		{"sin firma", payload, 0},
		{"payload cambiado", adminPayload + "." + sig, 0},
		{"firmada con otro secreto", foreign, 0},
		{"cookie de state", asState, 0},
		{"basura", "%%%.###", 0},
		{"caducada", good.Value, 7*24*time.Hour + time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h.clock.now = time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC).Add(tc.later)
			res := h.do(t, http.MethodGet, "/api/0/issues/", []*http.Cookie{{Name: sessionCookie, Value: tc.value}}, nil)
			if res.StatusCode != http.StatusUnauthorized {
				t.Errorf("= %d", res.StatusCode)
			}
		})
	}
	h.clock.now = time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC).Add(7*24*time.Hour - time.Minute)
	if res := h.do(t, http.MethodGet, "/api/0/issues/", []*http.Cookie{good}, nil); res.StatusCode != http.StatusOK {
		t.Errorf("antes de caducar = %d", res.StatusCode)
	}
}

func TestOrigin(t *testing.T) {
	gh := newFakeGitHub(t)
	h := newHarness(t, githubConfig(gh))
	s := cookieNamed(h.login(t, "ana", "/"), sessionCookie)
	for _, tc := range []struct {
		method, origin string
		want           int
	}{
		{http.MethodGet, "", http.StatusOK},
		{http.MethodGet, "https://otro.example", http.StatusOK},
		{http.MethodPost, publicURL, http.StatusOK},
		{http.MethodPost, "HTTPS://ERRORS.EXAMPLE.COM", http.StatusOK},
		{http.MethodPost, "", http.StatusForbidden},
		{http.MethodPost, "https://otro.example", http.StatusForbidden},
		{http.MethodPost, "http://errors.example.com", http.StatusForbidden},
		{http.MethodPost, "null", http.StatusForbidden},
		{http.MethodDelete, "https://errors.example.com.otro.example", http.StatusForbidden},
	} {
		header := map[string]string{}
		if tc.origin != "" {
			header["Origin"] = tc.origin
		}
		res := h.do(t, tc.method, "/api/0/issues/1/status", []*http.Cookie{s}, header)
		if res.StatusCode != tc.want {
			t.Errorf("%s con Origin %q = %d; quiero %d", tc.method, tc.origin, res.StatusCode, tc.want)
		}
	}
	// Sin cookie, el Origin no importa: no hay sesión y es un 401.
	if res := h.do(t, http.MethodPost, "/api/0/issues/1/status", nil, map[string]string{"Origin": "https://otro.example"}); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("sin cookie = %d", res.StatusCode)
	}
}

func TestLogout(t *testing.T) {
	gh := newFakeGitHub(t)
	h := newHarness(t, githubConfig(gh))
	s := cookieNamed(h.login(t, "ana", "/"), sessionCookie)
	if res := h.do(t, http.MethodPost, "/auth/logout", []*http.Cookie{s}, map[string]string{"Origin": "https://otro.example"}); res.StatusCode != http.StatusForbidden {
		t.Errorf("logout desde otro origen = %d", res.StatusCode)
	}
	res := h.do(t, http.MethodPost, "/auth/logout", []*http.Cookie{s}, map[string]string{"Origin": publicURL})
	c := cookieNamed(res, sessionCookie)
	if res.StatusCode != http.StatusNoContent || c == nil || c.MaxAge >= 0 || c.Value != "" {
		t.Errorf("logout = %d %+v", res.StatusCode, c)
	}
}

func TestDevLogin(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:8080", "localhost:9000"} {
		h := newHarness(t, Config{ListenAddr: addr, DevLogin: true})
		res := h.do(t, http.MethodGet, "/auth/dev/login?next=/issues/7", nil, nil)
		s := cookieNamed(res, sessionCookie)
		if res.StatusCode != http.StatusFound || res.Header.Get("Location") != "/issues/7" || s == nil || s.Secure {
			t.Fatalf("%s: dev login = %d %s %+v", addr, res.StatusCode, res.Header.Get("Location"), s)
		}
		res = h.do(t, http.MethodGet, "/api/0/issues/", []*http.Cookie{s}, nil)
		if b, _ := io.ReadAll(res.Body); string(b) != "session:dev" {
			t.Errorf("%s: con la sesión de dev = %d %s", addr, res.StatusCode, b)
		}
		// Sin PUBLIC_URL valen 127.0.0.1 y localhost con el puerto de LISTEN_ADDR.
		_, port, _ := net.SplitHostPort(addr)
		for origin, want := range map[string]int{
			"http://127.0.0.1:" + port:    http.StatusOK,
			"http://localhost:" + port:    http.StatusOK,
			"http://localhost:1":          http.StatusForbidden,
			"https://localhost:" + port:   http.StatusForbidden,
			"http://evil.example:" + port: http.StatusForbidden,
		} {
			res = h.do(t, http.MethodPost, "/api/0/issues/1/status", []*http.Cookie{s}, map[string]string{"Origin": origin})
			if res.StatusCode != want {
				t.Errorf("%s: POST con Origin %s = %d; quiero %d", addr, origin, res.StatusCode, want)
			}
		}
		if res := h.do(t, http.MethodPost, "/auth/logout", []*http.Cookie{s}, map[string]string{"Origin": "http://localhost:" + port}); res.StatusCode != http.StatusNoContent {
			t.Errorf("%s: logout desde localhost = %d", addr, res.StatusCode)
		}
		res = h.do(t, http.MethodGet, "/auth/session", nil, nil)
		var body sessionJSON
		_ = json.NewDecoder(res.Body).Decode(&body)
		if body.User != nil || len(body.Methods) != 1 || body.Methods[0] != "dev" {
			t.Errorf("%s: /auth/session = %+v", addr, body)
		}
		if res := h.do(t, http.MethodGet, "/auth/github/login", nil, nil); res.Header.Get("Location") != "/login?error=config" {
			t.Errorf("%s: GitHub sin configurar = %s", addr, res.Header.Get("Location"))
		}
	}

	for _, addr := range []string{":8080", "0.0.0.0:8080", "192.168.1.20:8080", "[::]:8080", "errors.example.com:443", ""} {
		if _, err := New(Config{ListenAddr: addr, DevLogin: true}, quiet); err == nil {
			t.Errorf("UI_DEV_LOGIN con LISTEN_ADDR %q debería negarse", addr)
		}
	}

	gh := newFakeGitHub(t)
	h := newHarness(t, githubConfig(gh))
	if res := h.do(t, http.MethodGet, "/auth/dev/login", nil, nil); res.StatusCode != http.StatusNotFound || cookieNamed(res, sessionCookie) != nil {
		t.Errorf("dev login sin UI_DEV_LOGIN = %d", res.StatusCode)
	}
}

func TestNotConfigured(t *testing.T) {
	h := newHarness(t, Config{ListenAddr: ":8080"})
	res := h.do(t, http.MethodGet, "/auth/session", nil, nil)
	var body sessionJSON
	_ = json.NewDecoder(res.Body).Decode(&body)
	if body.User != nil || body.Methods == nil || len(body.Methods) != 0 {
		t.Errorf("/auth/session = %+v", body)
	}
	if res := h.do(t, http.MethodGet, "/auth/github/login", nil, nil); res.Header.Get("Location") != "/login?error=config" {
		t.Errorf("login = %s", res.Header.Get("Location"))
	}
	// Una cookie, aunque esté bien firmada con cualquier cosa, no vale.
	if res := h.do(t, http.MethodGet, "/api/0/issues/", []*http.Cookie{{Name: sessionCookie, Value: "a.b"}}, nil); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("sin login configurado = %d", res.StatusCode)
	}
}

func TestNewConfig(t *testing.T) {
	gh := newFakeGitHub(t)
	ok := githubConfig(gh)
	for _, tc := range []struct {
		name string
		edit func(*Config)
	}{
		{"falta el secreto de GitHub", func(c *Config) { c.ClientSecret = "" }},
		{"falta la org", func(c *Config) { c.Org = "" }},
		{"falta SESSION_SECRET", func(c *Config) { c.SessionSecret = "" }},
		{"SESSION_SECRET corto", func(c *Config) { c.SessionSecret = "corto" }},
		{"falta PUBLIC_URL", func(c *Config) { c.PublicURL = "" }},
		{"PUBLIC_URL relativa", func(c *Config) { c.PublicURL = "errors.example.com" }},
	} {
		c := ok
		tc.edit(&c)
		if _, err := New(c, quiet); err == nil {
			t.Errorf("%s: debería fallar", tc.name)
		}
	}
	if _, err := New(ok, quiet); err != nil {
		t.Errorf("completa: %v", err)
	}

	env := map[string]string{
		"GITHUB_CLIENT_ID": "id", "GITHUB_CLIENT_SECRET": "s", "GITHUB_ORG": "example-org",
		"SESSION_SECRET": testSecret, "UI_DEV_LOGIN": "1",
	}
	c := LoadConfig(func(k string) string { return env[k] }, "127.0.0.1:8080", publicURL)
	if c.ClientID != "id" || c.ClientSecret != "s" || c.Org != "example-org" || !c.DevLogin || c.ListenAddr != "127.0.0.1:8080" || c.PublicURL != publicURL {
		t.Errorf("LoadConfig = %+v", c)
	}
	env["UI_DEV_LOGIN"] = "0"
	if LoadConfig(func(k string) string { return env[k] }, "", "").DevLogin {
		t.Error("UI_DEV_LOGIN=0 activa el modo de desarrollo")
	}
}

func TestSafeNext(t *testing.T) {
	for in, want := range map[string]string{
		"":                       "/",
		"/":                      "/",
		"/issues/42":             "/issues/42",
		"/?status=all&project=1": "/?status=all&project=1",
		"//evil.example":         "/",
		"/\\evil.example":        "/",
		"https://evil.example":   "/",
		"issues/1":               "/",
		"/auth/logout":           "/",
	} {
		if got := safeNext(in); got != want {
			t.Errorf("safeNext(%q) = %q; quiero %q", in, got, want)
		}
	}
}

// Con PUBLIC_URL en desarrollo, solo vale su origen.
func TestDevLoginWithPublicURL(t *testing.T) {
	h := newHarness(t, Config{ListenAddr: "127.0.0.1:8080", DevLogin: true, PublicURL: "http://localhost:4200"})
	s := cookieNamed(h.do(t, http.MethodGet, "/auth/dev/login", nil, nil), sessionCookie)
	for origin, want := range map[string]int{
		"http://localhost:4200": http.StatusOK,
		"http://127.0.0.1:8080": http.StatusForbidden,
		"http://localhost:8080": http.StatusForbidden,
	} {
		res := h.do(t, http.MethodPost, "/api/0/issues/1/status", []*http.Cookie{s}, map[string]string{"Origin": origin})
		if res.StatusCode != want {
			t.Errorf("Origin %s = %d; quiero %d", origin, res.StatusCode, want)
		}
	}
}
