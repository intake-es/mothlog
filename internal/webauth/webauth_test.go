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

	"github.com/intake-es/mothlog/internal/api"
)

const (
	testSecret = "0123456789abcdef0123456789abcdef"
	publicURL  = "https://errors.example.com"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// fakeGitHub hace de github.com y de api.github.com: cada código es una
// cuenta, con su id y su login.
type fakeGitHub struct {
	*httptest.Server
	mu        sync.Mutex
	exchanges []url.Values
	accounts  map[string]ghAccount
}

type ghAccount struct {
	id     int64
	login  string
	code   int
	state  string
	noName bool
}

// account añade una cuenta, miembro activo de la org, que entra con code.
func (gh *fakeGitHub) account(code string, id int64, login string) {
	gh.mu.Lock()
	defer gh.mu.Unlock()
	gh.accounts[code] = ghAccount{id: id, login: login, code: http.StatusOK, state: "active"}
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	t.Helper()
	gh := &fakeGitHub{accounts: map[string]ghAccount{
		"ana":   {101, "ana", http.StatusOK, "active", false},
		"pepe":  {102, "pepe", http.StatusOK, "pending", false},
		"eve":   {103, "eve", http.StatusNotFound, "", false},
		"rota":  {104, "rota", http.StatusInternalServerError, "", false},
		"sinid": {0, "sinid", http.StatusOK, "active", false},
	}}
	members := func(code string) (ghAccount, bool) {
		gh.mu.Lock()
		defer gh.mu.Unlock()
		a, ok := gh.accounts[code]
		return a, ok
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
		a, ok := members(user(r))
		if !ok {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		body := map[string]any{"login": a.login, "name": strings.ToUpper(a.login[:1]) + a.login[1:],
			"avatar_url": "https://avatars.example.com/" + a.login}
		if a.id != 0 {
			body["id"] = a.id
		}
		_ = json.NewEncoder(w).Encode(body)
	})
	mux.HandleFunc("GET /user/memberships/orgs/{org}", func(w http.ResponseWriter, r *http.Request) {
		m, _ := members(user(r))
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
	// host es el Host de las peticiones; con UI_DEV_LOGIN, uno de loopback.
	host string
}

func newHarness(t *testing.T, cfg Config) *harness {
	t.Helper()
	c := &clock{now: time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)}
	cfg.Now = c.Now
	a, err := New(cfg, quiet)
	if err != nil {
		t.Fatal(err)
	}
	a.SavePeople(&savedPeople{})
	h := &harness{auth: a, clock: c}
	if cfg.DevLogin {
		_, port, _ := net.SplitHostPort(cfg.ListenAddr)
		h.host = "127.0.0.1:" + port
	}
	h.mux = h.routes(a)
	return h
}

// routes son las de /auth y una /api/0/ protegida que responde quién es.
func (h *harness) routes(a *Auth) *http.ServeMux {
	mux := http.NewServeMux()
	a.Register(mux)
	protected := api.RequireAuth(a.Authenticator())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, _ := api.PrincipalFrom(r.Context())
		_, _ = w.Write([]byte(p.Kind + ":" + p.Name))
	}))
	mux.Handle("/api/0/", protected)
	return mux
}

// devLogin entra con la clave de desarrollo buena.
func (h *harness) devLogin(t *testing.T, next string) *http.Response {
	t.Helper()
	q := url.Values{"key": {h.auth.devKey}}
	if next != "" {
		q.Set("next", next)
	}
	return h.do(t, http.MethodGet, "/auth/dev/login?"+q.Encode(), nil, nil)
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
	if h.host != "" {
		req.Host = h.host
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	for k, v := range header {
		if k == "Host" {
			req.Host = v
			continue
		}
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.mux.ServeHTTP(rec, req)
	return rec.Result()
}

// cookieNamed busca la cookie; la de sesión, con o sin el prefijo __Host-.
func cookieNamed(res *http.Response, name string) *http.Cookie {
	for _, c := range res.Cookies() {
		if c.Name == name || (name == sessionCookie && c.Name == "__Host-"+sessionCookie) {
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
	if s == nil || s.Name != "__Host-ie_session" || !s.HttpOnly || !s.Secure || s.Domain != "" ||
		s.SameSite != http.SameSiteLaxMode || s.Path != "/" || s.MaxAge != 7*24*3600 {
		t.Fatalf("cookie de sesión = %+v", s)
	}
	// La de antes, sin prefijo, ya no se lee.
	if res := h.do(t, http.MethodGet, "/api/0/issues/", []*http.Cookie{{Name: sessionCookie, Value: s.Value}}, nil); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("la cookie sin __Host- = %d", res.StatusCode)
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
	issued := h.clock.now.UnixMicro()
	foreign, _ := otherAuth.sign("session", Session{Login: "ana", IssuedAt: issued, Expires: h.clock.now.Add(time.Hour).Unix()})
	admin, _ := h.auth.sign("session", Session{Login: "admin", IssuedAt: issued, Expires: h.clock.now.Add(time.Hour).Unix()})
	adminPayload, _, _ := strings.Cut(admin, ".")
	asState, _ := h.auth.sign("state", Session{Login: "ana", IssuedAt: issued, Expires: h.clock.now.Add(time.Hour).Unix()})
	// Las cookies de antes no llevaban fecha de emisión: se rechazan.
	undated, _ := h.auth.sign("session", Session{Login: "ana", Expires: h.clock.now.Add(time.Hour).Unix()})

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
		{"sin fecha de emisión", undated, 0},
		{"caducada", good.Value, 7*24*time.Hour + time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h.clock.now = time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC).Add(tc.later)
			res := h.do(t, http.MethodGet, "/api/0/issues/", []*http.Cookie{{Name: good.Name, Value: tc.value}}, nil)
			if res.StatusCode != http.StatusUnauthorized {
				t.Errorf("= %d", res.StatusCode)
			}
		})
	}
	h.clock.now = time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC).Add(7*24*time.Hour - time.Minute)
	if res := h.do(t, http.MethodGet, "/api/0/issues/", []*http.Cookie{good}, nil); res.StatusCode != http.StatusOK {
		t.Errorf("antes de caducar = %d", res.StatusCode)
	}

	// Si no se puede mirar si está revocada, no hay sesión.
	people := &savedPeople{}
	h.auth.SavePeople(people)
	fresh := cookieNamed(h.login(t, "ana", "/"), sessionCookie)
	people.readFails = true
	if res := h.do(t, http.MethodGet, "/api/0/issues/", []*http.Cookie{fresh}, nil); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("con la base caída = %d", res.StatusCode)
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
	h.clock.now = h.clock.now.Add(time.Second)
	res := h.do(t, http.MethodPost, "/auth/logout", []*http.Cookie{s}, map[string]string{"Origin": publicURL})
	c := cookieNamed(res, sessionCookie)
	if res.StatusCode != http.StatusNoContent || c == nil || c.MaxAge >= 0 || c.Value != "" {
		t.Errorf("logout = %d %+v", res.StatusCode, c)
	}
	// Quien guardó la cookie antes de cerrar sesión ya no entra con ella.
	if res := h.do(t, http.MethodGet, "/api/0/issues/", []*http.Cookie{s}, nil); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("la cookie de antes de cerrar sesión = %d", res.StatusCode)
	}
	h.clock.now = h.clock.now.Add(time.Second)
	again := cookieNamed(h.login(t, "ana", "/"), sessionCookie)
	if res := h.do(t, http.MethodGet, "/api/0/issues/", []*http.Cookie{again}, nil); res.StatusCode != http.StatusOK {
		t.Errorf("una sesión nueva = %d", res.StatusCode)
	}

	// Con la base caída no se puede revocar: el logout no dice 204, y en
	// cuanto la base vuelve la cookie sigue sin revocar, como avisó el 500.
	people := &savedPeople{}
	h.auth.SavePeople(people)
	h.clock.now = h.clock.now.Add(time.Second)
	down := cookieNamed(h.login(t, "ana", "/"), sessionCookie)
	people.readFails = true
	res = h.do(t, http.MethodPost, "/auth/logout", []*http.Cookie{down}, map[string]string{"Origin": publicURL})
	if res.StatusCode != http.StatusInternalServerError {
		t.Errorf("logout con la base caída = %d; quiero 500", res.StatusCode)
	}
	people.readFails = false
	h.clock.now = h.clock.now.Add(time.Second)
	res = h.do(t, http.MethodPost, "/auth/logout", []*http.Cookie{down}, map[string]string{"Origin": publicURL})
	if res.StatusCode != http.StatusNoContent {
		t.Errorf("logout de nuevo = %d", res.StatusCode)
	}
	if res := h.do(t, http.MethodGet, "/api/0/issues/", []*http.Cookie{down}, nil); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("tras reintentar el logout = %d", res.StatusCode)
	}
}

func TestDevLoginKeyAndHost(t *testing.T) {
	var logged strings.Builder
	c := &clock{now: time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)}
	a, err := New(Config{ListenAddr: "127.0.0.1:8080", DevLogin: true, Now: c.Now}, slog.New(slog.NewTextHandler(&logged, nil)))
	if err != nil {
		t.Fatal(err)
	}
	a.SavePeople(&savedPeople{})
	h := &harness{auth: a, clock: c, host: "127.0.0.1:8080"}
	h.mux = h.routes(a)

	// La clave es aleatoria, de 64 hexadecimales, y sale una vez en el log como enlace.
	link := "http://127.0.0.1:8080/auth/dev/login?key=" + a.devKey
	if len(a.devKey) != 64 || strings.Count(logged.String(), a.devKey) != 1 || !strings.Contains(logged.String(), link) {
		t.Errorf("clave %q; log = %s", a.devKey, logged.String())
	}
	other := newHarness(t, Config{ListenAddr: "127.0.0.1:8080", DevLogin: true})
	if other.auth.devKey == a.devKey {
		t.Error("dos arranques con la misma clave")
	}

	for _, tc := range []struct{ name, target, location string }{
		{"sin clave", "/auth/dev/login", "/login?error=dev_key"},
		{"clave vacía", "/auth/dev/login?key=", "/login?error=dev_key"},
		{"clave mala", "/auth/dev/login?key=" + strings.Repeat("0", 64), "/login?error=dev_key"},
		{"clave de otro arranque", "/auth/dev/login?key=" + other.auth.devKey, "/login?error=dev_key"},
		{"media clave", "/auth/dev/login?key=" + a.devKey[:32], "/login?error=dev_key"},
	} {
		res := h.do(t, http.MethodGet, tc.target, nil, nil)
		if res.StatusCode != http.StatusFound || res.Header.Get("Location") != tc.location || cookieNamed(res, sessionCookie) != nil {
			t.Errorf("%s = %d %s %v", tc.name, res.StatusCode, res.Header.Get("Location"), res.Cookies())
		}
	}

	// Con la clave, solo los Host de loopback con el puerto de LISTEN_ADDR.
	for host, ok := range map[string]bool{
		"127.0.0.1:8080":      true,
		"localhost:8080":      true,
		"LOCALHOST:8080":      true,
		"[::1]:8080":          true,
		"proxy:8080":          false,
		"rebind.example:8080": false,
		"127.0.0.1:9999":      false,
		"localhost":           false,
		"":                    false,
	} {
		res := h.do(t, http.MethodGet, "/auth/dev/login?key="+a.devKey, nil, map[string]string{"Host": host})
		got := cookieNamed(res, sessionCookie) != nil
		if got != ok || (!ok && res.StatusCode != http.StatusForbidden) {
			t.Errorf("Host %q: %d, sesión %v; quiero %v", host, res.StatusCode, got, ok)
		}
	}

	// Una sesión buena no vale si llega con otro Host (DNS rebinding).
	s := cookieNamed(h.devLogin(t, "/"), sessionCookie)
	for host, want := range map[string]int{
		"127.0.0.1:8080":      http.StatusOK,
		"[::1]:8080":          http.StatusOK,
		"rebind.example:8080": http.StatusUnauthorized,
	} {
		res := h.do(t, http.MethodGet, "/api/0/issues/", []*http.Cookie{s}, map[string]string{"Host": host})
		if res.StatusCode != want {
			t.Errorf("/api/0 con Host %s = %d; quiero %d", host, res.StatusCode, want)
		}
	}
	res := h.do(t, http.MethodGet, "/auth/session", []*http.Cookie{s}, map[string]string{"Host": "rebind.example:8080"})
	var body sessionJSON
	_ = json.NewDecoder(res.Body).Decode(&body)
	if body.User != nil {
		t.Errorf("/auth/session con un Host ajeno = %+v", body.User)
	}

	// Con PUBLIC_URL vale además su Host.
	pub := newHarness(t, Config{ListenAddr: "127.0.0.1:8080", DevLogin: true, PublicURL: "http://localhost:4200"})
	for host, ok := range map[string]bool{"localhost:4200": true, "127.0.0.1:8080": true, "evil.example:4200": false} {
		pub.host = host
		if got := cookieNamed(pub.devLogin(t, ""), sessionCookie) != nil; got != ok {
			t.Errorf("con PUBLIC_URL, Host %s: sesión %v", host, got)
		}
	}
}

func TestDevLogin(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:8080", "localhost:9000"} {
		h := newHarness(t, Config{ListenAddr: addr, DevLogin: true})
		res := h.devLogin(t, "/issues/7")
		s := cookieNamed(res, sessionCookie)
		if res.StatusCode != http.StatusFound || res.Header.Get("Location") != "/issues/7" || s == nil || s.Secure || s.Name != sessionCookie {
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
		{"PUBLIC_URL http", func(c *Config) { c.PublicURL = "http://errors.example.com" }},
		{"PUBLIC_URL http en la red local", func(c *Config) { c.PublicURL = "http://192.168.1.20:8080" }},
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
	for _, local := range []string{"http://localhost:8080", "http://127.0.0.1:8080", "http://[::1]:8080"} {
		c := ok
		c.PublicURL = local
		if _, err := New(c, quiet); err != nil {
			t.Errorf("PUBLIC_URL %s en loopback: %v", local, err)
		}
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
		"":                        "/",
		"/":                       "/",
		"/issues/42":              "/issues/42",
		"/?status=all&project=1":  "/?status=all&project=1",
		"//evil.example":          "/",
		"/\\evil.example":         "/",
		"https://evil.example":    "/",
		"issues/1":                "/",
		"/auth/logout":            "/",
		"/auth":                   "/",
		"/./auth/dev/login":       "/",
		"/x/../auth/github/login": "/",
		"/%2e/auth/dev/login":     "/",
		"/%2E%2E/auth/oidc/login": "/",
		"/x/%2e%2e/auth/logout":   "/",
		"/%61uth/dev/login":       "/",
		"/.//evil.example":        "/",
		"/..//evil.example":       "/",
		"/x/..//evil.example":     "/",
		"/%2F/evil.example":       "/",
		"///evil.example":         "/",
		"/\t/evil.example":        "/",
		"/authors":                "/authors",
		"/issues/1?next=/auth/x":  "/issues/1?next=/auth/x",
	} {
		if got := safeNext(in); got != want {
			t.Errorf("safeNext(%q) = %q; quiero %q", in, got, want)
		}
	}
}

// Con PUBLIC_URL en desarrollo, solo vale su origen.
func TestDevLoginWithPublicURL(t *testing.T) {
	h := newHarness(t, Config{ListenAddr: "127.0.0.1:8080", DevLogin: true, PublicURL: "http://localhost:4200"})
	h.host = "localhost:4200"
	s := cookieNamed(h.devLogin(t, ""), sessionCookie)
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
