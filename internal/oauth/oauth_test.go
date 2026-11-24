package oauth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/intake-es/intake-errors/internal/api"
	"github.com/intake-es/intake-errors/internal/netguard"
	"github.com/intake-es/intake-errors/internal/store"
	"github.com/intake-es/intake-errors/internal/store/storetest"
)

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

type harness struct {
	st    *storetest.DB
	base  string
	clock *clock
	oa    *Server
	http  *http.Client
}

// newHarness monta el OAuth y el MCP; la sesión del panel es la cabecera
// X-Login, en lugar de la cookie.
func newHarness(t *testing.T, client *http.Client) *harness {
	t.Helper()
	st := storetest.NewDB(t)
	ctx := context.Background()
	for _, login := range []string{"ana", "luis"} {
		if _, err := st.SaveIdentity(ctx, store.Person{Login: login, Provider: store.LoginDev, Subject: login}); err != nil {
			t.Fatal(err)
		}
	}
	mux := http.NewServeMux()
	srv := httptest.NewUnstartedServer(mux)
	base := "http://" + srv.Listener.Addr().String()
	c := &clock{now: time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)}
	oa, err := New(st.Store, Config{PublicURL: base, Now: c.Now, HTTPClient: client, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	session := func(r *http.Request) (api.Principal, bool, error) {
		if l := r.Header.Get("X-Login"); l != "" {
			return api.Principal{Kind: "session", Name: l}, true, nil
		}
		return api.Principal{}, false, nil
	}
	oa.Register(mux, api.RequireAuth(session))
	if err := api.Register(mux, st.Store, api.Options{
		PublicURL: base,
		MCPAuth:   api.RequireAuthChallenge(oa.Challenge, oa.Authenticator(), api.BearerToken(st.Store)),
	}); err != nil {
		t.Fatal(err)
	}
	srv.Start()
	t.Cleanup(srv.Close)
	return &harness{st: st, base: base, clock: c, oa: oa, http: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}}
}

type reply struct {
	code   int
	header http.Header
	body   []byte
}

func (h *harness) do(t *testing.T, method, path string, body io.Reader, header map[string]string) reply {
	t.Helper()
	req, err := http.NewRequest(method, h.base+path, body)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	res, err := h.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	b, _ := io.ReadAll(res.Body)
	return reply{res.StatusCode, res.Header, b}
}

func (h *harness) register(t *testing.T, redirects ...string) string {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"client_name": "Prueba", "redirect_uris": redirects})
	r := h.do(t, http.MethodPost, "/oauth/register", bytes.NewReader(b), nil)
	var out struct {
		ClientID string `json:"client_id"`
	}
	if err := json.Unmarshal(r.body, &out); err != nil || r.code != http.StatusCreated {
		t.Fatalf("registro = %d %s", r.code, r.body)
	}
	return out.ClientID
}

func pkce(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

const verifier = "verificador-de-pkce-con-mas-de-43-caracteres-0123456789"

func (h *harness) authorizeQuery(clientID, redirect string) url.Values {
	return url.Values{
		"response_type": {"code"}, "client_id": {clientID}, "redirect_uri": {redirect}, "scope": {"read write"},
		"state": {"e"}, "code_challenge": {pkce(verifier)}, "code_challenge_method": {"S256"}, "resource": {h.base + "/mcp"},
	}
}

// startAuth devuelve el id de la petición pendiente.
func (h *harness) startAuth(t *testing.T, q url.Values) string {
	t.Helper()
	r := h.do(t, http.MethodGet, "/oauth/authorize?"+q.Encode(), nil, nil)
	loc := r.header.Get("Location")
	if r.code != http.StatusFound || !strings.HasPrefix(loc, h.base+"/authorize?request=") {
		t.Fatalf("autorizar = %d %q", r.code, loc)
	}
	return strings.TrimPrefix(loc, h.base+"/authorize?request=")
}

func (h *harness) approve(t *testing.T, id, login string, scopes ...string) (int, string) {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"scopes": scopes})
	r := h.do(t, http.MethodPost, "/api/0/oauth/requests/"+id+"/approve", bytes.NewReader(b), map[string]string{"X-Login": login})
	var out struct {
		RedirectTo string `json:"redirect_to"`
	}
	_ = json.Unmarshal(r.body, &out)
	if out.RedirectTo == "" {
		return r.code, ""
	}
	u, _ := url.Parse(out.RedirectTo)
	return r.code, u.Query().Get("code")
}

// code hace el recorrido hasta el código con el consentimiento de ana.
func (h *harness) code(t *testing.T, clientID, redirect string, scopes ...string) string {
	t.Helper()
	code, c := h.approve(t, h.startAuth(t, h.authorizeQuery(clientID, redirect)), "ana", scopes...)
	if code != http.StatusOK || c == "" {
		t.Fatalf("consentir = %d", code)
	}
	return c
}

type tokenReply struct {
	Access  string `json:"access_token"`
	Refresh string `json:"refresh_token"`
	Scope   string `json:"scope"`
	Error   string `json:"error"`
	Desc    string `json:"error_description"`
}

func (h *harness) token(t *testing.T, form url.Values) (int, tokenReply) {
	t.Helper()
	r := h.do(t, http.MethodPost, "/oauth/token", strings.NewReader(form.Encode()),
		map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
	var out tokenReply
	if err := json.Unmarshal(r.body, &out); err != nil {
		t.Fatalf("token = %d %s", r.code, r.body)
	}
	return r.code, out
}

func exchangeForm(clientID, redirect, code, v string) url.Values {
	return url.Values{"grant_type": {"authorization_code"}, "client_id": {clientID}, "redirect_uri": {redirect},
		"code": {code}, "code_verifier": {v}}
}

func (h *harness) mcpCode(t *testing.T, token string) (int, string) {
	t.Helper()
	r := h.do(t, http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`), map[string]string{
		"Authorization": "Bearer " + token, "Content-Type": "application/json", "Accept": "application/json, text/event-stream",
	})
	return r.code, r.header.Get("WWW-Authenticate")
}

func TestAuthorizeRejects(t *testing.T) {
	h := newHarness(t, nil)
	redirect := "http://127.0.0.1:5555/cb"
	id := h.register(t, redirect)

	toPanel := func(name string, q url.Values, reason string) {
		t.Helper()
		r := h.do(t, http.MethodGet, "/oauth/authorize?"+q.Encode(), nil, nil)
		if want := h.base + "/authorize?error=" + reason; r.code != http.StatusFound || r.header.Get("Location") != want {
			t.Errorf("%s = %d %q, quiero %q", name, r.code, r.header.Get("Location"), want)
		}
	}
	toClient := func(name string, q url.Values, kind string) {
		t.Helper()
		r := h.do(t, http.MethodGet, "/oauth/authorize?"+q.Encode(), nil, nil)
		u, err := url.Parse(r.header.Get("Location"))
		if r.code != http.StatusFound || err != nil || !strings.HasPrefix(u.String(), redirect+"?") ||
			u.Query().Get("error") != kind || u.Query().Get("state") != "e" || u.Query().Get("iss") != h.base {
			t.Errorf("%s = %d %q, quiero error=%s", name, r.code, r.header.Get("Location"), kind)
		}
	}
	with := func(k, v string) url.Values {
		q := h.authorizeQuery(id, redirect)
		if v == "" {
			q.Del(k)
		} else {
			q.Set(k, v)
		}
		return q
	}

	toPanel("cliente desconocido", h.authorizeQuery("iec_no-existe", redirect), "client")
	toPanel("sin redirect_uri, aunque solo tenga una", with("redirect_uri", ""), "redirect")
	toPanel("otra ruta", with("redirect_uri", "http://127.0.0.1:5555/otra"), "redirect")
	toPanel("otro sitio", with("redirect_uri", "https://atacante.example.com/cb"), "redirect")
	toPanel("localhost en vez de la IP", with("redirect_uri", "http://localhost:5555/cb"), "redirect")
	toClient("sin PKCE", with("code_challenge", ""), "invalid_request")
	toClient("PKCE plain", with("code_challenge_method", "plain"), "invalid_request")
	toClient("PKCE sin método", with("code_challenge_method", ""), "invalid_request")
	toClient("challenge corto", with("code_challenge", "abc"), "invalid_request")
	toClient("sin resource", with("resource", ""), "invalid_target")
	toClient("resource de otro sitio", with("resource", "https://otro.example.com/mcp"), "invalid_target")
	toClient("resource de otra ruta", with("resource", h.base+"/api"), "invalid_target")
	toClient("response_type token", with("response_type", "token"), "unsupported_response_type")

	// En loopback por IP, el puerto puede cambiar (RFC 8252).
	h.startAuth(t, with("redirect_uri", "http://127.0.0.1:6666/cb"))
	// Caducado el registro sin usar, el cliente ya no existe.
	h.clock.add(25 * time.Hour)
	toPanel("cliente caducado", h.authorizeQuery(id, redirect), "client")
}

func TestRegisterRejects(t *testing.T) {
	h := newHarness(t, nil)
	for name, body := range map[string]string{
		"http a otro sitio":  `{"redirect_uris":["http://cliente.example.com/cb"]}`,
		"esquema propio":     `{"redirect_uris":["cursor://cb"]}`,
		"con fragmento":      `{"redirect_uris":["https://cliente.example.com/cb#x"]}`,
		"relativa":           `{"redirect_uris":["/cb"]}`,
		"sin URIs":           `{"client_name":"x"}`,
		"grant implícito":    `{"redirect_uris":["https://c.example.com/cb"],"grant_types":["implicit"]}`,
		"no es JSON":         `redirect_uris=x`,
		"usuario en la URI":  `{"redirect_uris":["https://u:p@c.example.com/cb"]}`,
		"response_type malo": `{"redirect_uris":["https://c.example.com/cb"],"response_types":["token"]}`,
	} {
		if r := h.do(t, http.MethodPost, "/oauth/register", strings.NewReader(body), nil); r.code != http.StatusBadRequest {
			t.Errorf("%s = %d %s", name, r.code, r.body)
		}
	}
	// Un nombre con caracteres de control se limpia; sin nombre, el sitio.
	r := h.do(t, http.MethodPost, "/oauth/register", strings.NewReader(`{"client_name":"Mi\u202e app\n","redirect_uris":["http://[::1]:9/cb"]}`), nil)
	if r.code != http.StatusCreated || !strings.Contains(string(r.body), `"client_name":"Mi app"`) {
		t.Errorf("nombre limpio = %d %s", r.code, r.body)
	}
	r = h.do(t, http.MethodPost, "/oauth/register", strings.NewReader(`{"redirect_uris":["https://c.example.com/cb"]}`), nil)
	if r.code != http.StatusCreated || !strings.Contains(string(r.body), `"client_name":"c.example.com"`) {
		t.Errorf("sin nombre = %d %s", r.code, r.body)
	}
}

func TestTokenRejects(t *testing.T) {
	h := newHarness(t, nil)
	redirect := "https://cliente.example.com/cb"
	id := h.register(t, redirect)
	invalid := func(name string, code int, got tokenReply, kind string) {
		t.Helper()
		if code != http.StatusBadRequest || got.Error != kind {
			t.Errorf("%s = %d %+v, quiero %s", name, code, got, kind)
		}
	}

	c := h.code(t, id, redirect, "read")
	code, got := h.token(t, exchangeForm(id, redirect, c, ""))
	invalid("sin code_verifier", code, got, "invalid_request")
	code, got = h.token(t, exchangeForm(id, redirect, c, strings.Repeat("x", 43)))
	invalid("code_verifier que no es", code, got, "invalid_grant")
	// El código fallido queda gastado.
	code, got = h.token(t, exchangeForm(id, redirect, c, verifier))
	invalid("código gastado por un PKCE malo", code, got, "invalid_grant")

	c = h.code(t, id, redirect, "read")
	code, got = h.token(t, exchangeForm(id, "https://cliente.example.com/otra", c, verifier))
	invalid("redirect_uri distinta", code, got, "invalid_grant")

	other := h.register(t, redirect)
	c = h.code(t, id, redirect, "read")
	code, got = h.token(t, exchangeForm(other, redirect, c, verifier))
	invalid("código de otro cliente", code, got, "invalid_grant")

	c = h.code(t, id, redirect, "read")
	f := exchangeForm(id, redirect, c, verifier)
	f.Set("resource", "https://otro.example.com/mcp")
	code, got = h.token(t, f)
	if code != http.StatusBadRequest || got.Error != "invalid_target" {
		t.Errorf("resource de otro sitio = %d %+v", code, got)
	}

	c = h.code(t, id, redirect, "read")
	h.clock.add(codeTTL + time.Second)
	code, got = h.token(t, exchangeForm(id, redirect, c, verifier))
	invalid("código caducado", code, got, "invalid_grant")

	// Un código reusado revoca lo que salió de él.
	c = h.code(t, id, redirect, "read")
	code, first := h.token(t, exchangeForm(id, redirect, c, verifier))
	if code != http.StatusOK {
		t.Fatalf("canje = %d %+v", code, first)
	}
	if st, _ := h.mcpCode(t, first.Access); st != http.StatusOK {
		t.Fatalf("/mcp = %d", st)
	}
	code, got = h.token(t, exchangeForm(id, redirect, c, verifier))
	invalid("código reusado", code, got, "invalid_grant")
	if st, _ := h.mcpCode(t, first.Access); st != http.StatusUnauthorized {
		t.Errorf("/mcp tras reusar el código = %d, quiero 401", st)
	}

	// Un token de acceso caducado da 401 con invalid_token; el refresco sigue.
	c = h.code(t, id, redirect, "read")
	_, tok := h.token(t, exchangeForm(id, redirect, c, verifier))
	h.clock.add(DefaultTTL.Access + time.Second)
	if st, challenge := h.mcpCode(t, tok.Access); st != http.StatusUnauthorized || !strings.Contains(challenge, `error="invalid_token"`) ||
		!strings.Contains(challenge, `resource_metadata="`+h.base+`/.well-known/oauth-protected-resource/mcp"`) {
		t.Errorf("acceso caducado = %d %q", st, challenge)
	}
	code, fresh := h.token(t, url.Values{"grant_type": {"refresh_token"}, "client_id": {id}, "refresh_token": {tok.Refresh}})
	if code != http.StatusOK {
		t.Fatalf("refresco = %d %+v", code, fresh)
	}
	if st, _ := h.mcpCode(t, fresh.Access); st != http.StatusOK {
		t.Errorf("/mcp con el acceso nuevo = %d", st)
	}
	// El refresco de otro cliente no vale.
	code, got = h.token(t, url.Values{"grant_type": {"refresh_token"}, "client_id": {other}, "refresh_token": {fresh.Refresh}})
	invalid("refresco de otro cliente", code, got, "invalid_grant")
	code, got = h.token(t, url.Values{"grant_type": {"client_credentials"}, "client_id": {id}})
	if code != http.StatusBadRequest || got.Error != "unsupported_grant_type" {
		t.Errorf("client_credentials = %d %+v", code, got)
	}
}

func TestConsent(t *testing.T) {
	h := newHarness(t, nil)
	redirect := "https://cliente.example.com/cb"
	id := h.register(t, redirect)

	// Solo lo pedido: con scope=read, marcar write no lo concede.
	q := h.authorizeQuery(id, redirect)
	q.Set("scope", "read offline_access")
	req := h.startAuth(t, q)
	r := h.do(t, http.MethodGet, "/api/0/oauth/requests/"+req, nil, map[string]string{"X-Login": "ana"})
	if r.code != http.StatusOK || !strings.Contains(string(r.body), `"scopes":["read"]`) {
		t.Fatalf("pendiente = %d %s", r.code, r.body)
	}
	if code, _ := h.approve(t, req, "ana"); code != http.StatusBadRequest {
		t.Errorf("sin marcar nada = %d, quiero 400", code)
	}
	code, c := h.approve(t, req, "ana", "read", "write")
	if code != http.StatusOK {
		t.Fatalf("consentir = %d", code)
	}
	if code, _ := h.approve(t, req, "ana", "read"); code != http.StatusNotFound {
		t.Errorf("consentir dos veces = %d, quiero 404", code)
	}
	_, tok := h.token(t, exchangeForm(id, redirect, c, verifier))
	if tok.Scope != "read" {
		t.Errorf("concedido = %q, quiero read", tok.Scope)
	}

	// Denegar vuelve al cliente con access_denied.
	req = h.startAuth(t, h.authorizeQuery(id, redirect))
	r = h.do(t, http.MethodPost, "/api/0/oauth/requests/"+req+"/deny", nil, map[string]string{"X-Login": "ana"})
	if r.code != http.StatusOK || !strings.Contains(string(r.body), "error=access_denied") {
		t.Errorf("denegar = %d %s", r.code, r.body)
	}

	// Una petición caduca a los 10 minutos.
	req = h.startAuth(t, h.authorizeQuery(id, redirect))
	h.clock.add(requestTTL + time.Second)
	if code, _ := h.approve(t, req, "ana", "read"); code != http.StatusNotFound {
		t.Errorf("consentir caducada = %d, quiero 404", code)
	}

	// Sin sesión del panel no se consiente, y un token ie_ no es una sesión.
	req = h.startAuth(t, h.authorizeQuery(id, redirect))
	if r := h.do(t, http.MethodPost, "/api/0/oauth/requests/"+req+"/approve", strings.NewReader(`{"scopes":["read"]}`), nil); r.code != http.StatusUnauthorized {
		t.Errorf("sin sesión = %d", r.code)
	}

	// Cada persona ve y revoca solo lo suyo.
	r = h.do(t, http.MethodGet, "/api/0/oauth/grants/", nil, map[string]string{"X-Login": "luis"})
	if r.code != http.StatusOK || string(bytes.TrimSpace(r.body)) != `{"data":[]}` {
		t.Errorf("las de luis = %d %s", r.code, r.body)
	}
	gs, err := h.st.OAuthGrants(context.Background(), "ana", h.clock.Now())
	if err != nil || len(gs) != 1 {
		t.Fatalf("las de ana = %v %v", gs, err)
	}
	path := "/api/0/oauth/grants/" + strconv.FormatInt(gs[0].ID, 10)
	if r := h.do(t, http.MethodDelete, path, nil, map[string]string{"X-Login": "luis"}); r.code != http.StatusNotFound {
		t.Errorf("luis revoca la de ana = %d, quiero 404", r.code)
	}
	if r := h.do(t, http.MethodDelete, path, nil, map[string]string{"X-Login": "ana"}); r.code != http.StatusNoContent {
		t.Errorf("ana revoca la suya = %d", r.code)
	}
}

func TestRevokeEndpoint(t *testing.T) {
	h := newHarness(t, nil)
	redirect := "https://cliente.example.com/cb"
	id := h.register(t, redirect)
	other := h.register(t, redirect)
	_, tok := h.token(t, exchangeForm(id, redirect, h.code(t, id, redirect, "read"), verifier))
	revoke := func(token, client string) int {
		r := h.do(t, http.MethodPost, "/oauth/revoke", strings.NewReader(url.Values{"token": {token}, "client_id": {client}}.Encode()),
			map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
		return r.code
	}
	if code := revoke("ier_no-existe", id); code != http.StatusOK {
		t.Errorf("token que no existe = %d, quiero 200", code)
	}
	if code := revoke(tok.Access, other); code != http.StatusOK {
		t.Errorf("de otro cliente = %d", code)
	}
	if st, _ := h.mcpCode(t, tok.Access); st != http.StatusOK {
		t.Fatalf("otro cliente no puede revocar: /mcp = %d", st)
	}
	// Revocar el acceso deja el refresco.
	revoke(tok.Access, id)
	if st, _ := h.mcpCode(t, tok.Access); st != http.StatusUnauthorized {
		t.Errorf("/mcp con el acceso revocado = %d", st)
	}
	code, fresh := h.token(t, url.Values{"grant_type": {"refresh_token"}, "client_id": {id}, "refresh_token": {tok.Refresh}})
	if code != http.StatusOK {
		t.Fatalf("refresco tras revocar el acceso = %d %+v", code, fresh)
	}
}

func metadataServer(t *testing.T, doc func(base string) string) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/client.json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, doc(srv.URL))
		case "/grande.json":
			_, _ = io.WriteString(w, `{"client_id":"`+srv.URL+`/grande.json","x":"`+strings.Repeat("a", maxMetadataBytes)+`"}`)
		case "/redirige.json":
			http.Redirect(w, r, "/client.json", http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestClientMetadataDocument(t *testing.T) {
	doc := `{"client_id":"%s/client.json","client_name":"Cliente por URL","redirect_uris":["http://127.0.0.1:7777/cb"],"token_endpoint_auth_method":"none"}`
	md := metadataServer(t, func(base string) string { return strings.Replace(doc, "%s", base, 1) })
	h := newHarness(t, md.Client())
	cid := md.URL + "/client.json"

	req := h.startAuth(t, h.authorizeQuery(cid, "http://127.0.0.1:7777/cb"))
	r := h.do(t, http.MethodGet, "/api/0/oauth/requests/"+req, nil, map[string]string{"X-Login": "ana"})
	if r.code != http.StatusOK || !strings.Contains(string(r.body), `"client_name":"Cliente por URL"`) || !strings.Contains(string(r.body), `"client_url":true`) {
		t.Fatalf("pendiente = %d %s", r.code, r.body)
	}
	_, c := h.approve(t, req, "ana", "read")
	code, tok := h.token(t, exchangeForm(cid, "http://127.0.0.1:7777/cb", c, verifier))
	if code != http.StatusOK || tok.Scope != "read" {
		t.Fatalf("canje = %d %+v", code, tok)
	}

	ctx := context.Background()
	for name, id := range map[string]string{
		"demasiado grande":        md.URL + "/grande.json",
		"redirige":                md.URL + "/redirige.json",
		"no existe":               md.URL + "/nada.json",
		"sin ruta":                md.URL,
		"con fragmento":           md.URL + "/client.json#x",
		"con ..":                  md.URL + "/a/../client.json",
		"client_id que no es":     md.URL + "/client.json?x=1",
		"documento de otro sitio": "http://" + strings.TrimPrefix(md.URL, "https://") + "/client.json",
	} {
		if _, err := h.oa.fetchMetadata(ctx, id); !errors.Is(err, errClient) {
			t.Errorf("%s: %v, quiero errClient", name, err)
		}
	}
}

func TestClientMetadataPrivateAddress(t *testing.T) {
	md := metadataServer(t, func(base string) string {
		return `{"client_id":"` + base + `/client.json","redirect_uris":["https://c.example.com/cb"]}`
	})
	// Sin cliente propio, el de por defecto no pide a direcciones privadas.
	h := newHarness(t, nil)
	cid := md.URL + "/client.json"
	_, err := h.oa.fetchMetadata(context.Background(), cid)
	if !errors.Is(err, errClient) || !strings.Contains(err.Error(), "privada") {
		t.Fatalf("documento en 127.0.0.1 = %v", err)
	}
	r := h.do(t, http.MethodGet, "/oauth/authorize?"+h.authorizeQuery(cid, "https://c.example.com/cb").Encode(), nil, nil)
	if r.code != http.StatusFound || r.header.Get("Location") != h.base+"/authorize?error=client" {
		t.Errorf("autorizar = %d %q", r.code, r.header.Get("Location"))
	}
	// Tampoco con el nombre de una dirección de metadatos.
	if err := (netguard.Policy{}).Check(netip.MustParseAddr("169.254.169.254")); !errors.Is(err, netguard.ErrBlocked) {
		t.Errorf("metadatos = %v", err)
	}
}

func TestClientIP(t *testing.T) {
	for _, tc := range []struct {
		remote, xff, want string
	}{
		{"203.0.113.9:5000", "", "203.0.113.9"},
		{"203.0.113.9:5000", "198.51.100.1", "203.0.113.9"},
		{"127.0.0.1:5000", "198.51.100.1, 198.51.100.2", "198.51.100.2"},
		{"10.0.0.2:5000", "falso", "10.0.0.2"},
		{"[::1]:5000", "", "::1"},
	} {
		r := httptest.NewRequest(http.MethodGet, "/mcp", nil)
		r.RemoteAddr = tc.remote
		if tc.xff != "" {
			r.Header.Set("X-Forwarded-For", tc.xff)
		}
		if got := clientIP(r); got != tc.want {
			t.Errorf("clientIP(%s, %q) = %q, quiero %q", tc.remote, tc.xff, got, tc.want)
		}
	}
}

func TestNoPublicURL(t *testing.T) {
	oa, err := New(nil, Config{})
	if oa != nil || err != nil {
		t.Fatalf("sin PUBLIC_URL = %v, %v; quiero nil, nil", oa, err)
	}
	if _, err := New(nil, Config{PublicURL: "errors.example.com"}); err == nil {
		t.Error("una PUBLIC_URL sin esquema debería fallar")
	}
}

func TestRateLimitPerAddress(t *testing.T) {
	h := newHarness(t, nil)
	body := `{"redirect_uris":["https://c.example.com/cb"]}`
	for i := range maxRegistersPerIP {
		if r := h.do(t, http.MethodPost, "/oauth/register", strings.NewReader(body), nil); r.code != http.StatusCreated {
			t.Fatalf("registro %d = %d", i+1, r.code)
		}
	}
	r := h.do(t, http.MethodPost, "/oauth/register", strings.NewReader(body), nil)
	if r.code != http.StatusTooManyRequests || r.header.Get("Retry-After") == "" {
		t.Fatalf("registro de más = %d", r.code)
	}
	// Otra dirección no se queda sin registrar.
	l := newLimiter(1)
	now := time.Now()
	if !l.allow("198.51.100.1", now) || l.allow("198.51.100.1", now) || !l.allow("198.51.100.2", now) {
		t.Error("el límite tiene que ser por dirección")
	}
	if !l.allow("198.51.100.1", now.Add(limitWindow)) {
		t.Error("pasada la ventana, vuelve a poder")
	}

	id := h.register2(t)
	q := h.authorizeQuery(id, "https://c.example.com/cb")
	for i := range maxAuthorizePerIP {
		if r := h.do(t, http.MethodGet, "/oauth/authorize?"+q.Encode(), nil, nil); r.code != http.StatusFound {
			t.Fatalf("autorización %d = %d", i+1, r.code)
		}
	}
	if r := h.do(t, http.MethodGet, "/oauth/authorize?"+q.Encode(), nil, nil); r.code != http.StatusTooManyRequests {
		t.Fatalf("autorización de más = %d", r.code)
	}
	h.clock.add(limitWindow)
	if r := h.do(t, http.MethodGet, "/oauth/authorize?"+q.Encode(), nil, nil); r.code != http.StatusFound {
		t.Fatalf("pasada la ventana = %d", r.code)
	}
}

// register2 registra tras pasar la ventana del límite de registros.
func (h *harness) register2(t *testing.T) string {
	t.Helper()
	h.clock.add(limitWindow)
	return h.register(t, "https://c.example.com/cb")
}
