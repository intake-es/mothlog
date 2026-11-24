package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/intake-es/mothlog/internal/ingest"
	"github.com/intake-es/mothlog/internal/store"
	"github.com/intake-es/mothlog/internal/store/storetest"
)

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// liveServer es el servicio entero, como lo arranca main, en un puerto libre
// de loopback, con UI_DEV_LOGIN y PUBLIC_URL.
type liveServer struct {
	base   string
	db     *storetest.DB
	devKey string
	// web lleva la cookie de la sesión del panel; http, nada.
	web  *http.Client
	http *http.Client
}

func noRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

func startServer(t *testing.T) *liveServer {
	t.Helper()
	for _, k := range []string{"GITHUB_CLIENT_ID", "GITHUB_CLIENT_SECRET", "GITHUB_ORG", "OIDC_ISSUER", "OIDC_CLIENT_ID",
		"OIDC_CLIENT_SECRET", "SESSION_SECRET"} {
		t.Setenv(k, "")
	}
	t.Setenv("UI_DEV_LOGIN", "1")
	dbURL := storetest.NewURL(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	base := "http://" + ln.Addr().String()
	cfg := config{DatabaseURL: dbURL, Addr: ln.Addr().String(), PublicURL: base, RateLimitPerMinute: 60, RetentionDays: 90,
		Batch: ingest.DefaultBatchOptions()}
	logs := &syncBuffer{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serve(ctx, cfg, slog.New(slog.NewJSONHandler(logs, nil)), ln) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil && err != http.ErrServerClosed {
			t.Errorf("serve: %v", err)
		}
	})
	for i := 0; ; i++ {
		res, err := http.Get(base + "/healthz")
		if err == nil {
			_ = res.Body.Close()
			if res.StatusCode == http.StatusOK {
				break
			}
		}
		if i > 200 {
			t.Fatalf("el servidor no arranca: %v\n%s", err, logs.String())
		}
		time.Sleep(25 * time.Millisecond)
	}
	m := regexp.MustCompile(`dev/login\?key=([0-9a-f]+)`).FindStringSubmatch(logs.String())
	if m == nil {
		t.Fatalf("no sale el enlace de UI_DEV_LOGIN en el log:\n%s", logs.String())
	}
	st, err := store.OpenForCommand(ctx, dbURL, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	jar, _ := cookiejar.New(nil)
	return &liveServer{
		base: base, db: &storetest.DB{Store: st}, devKey: m[1],
		web:  &http.Client{Jar: jar, CheckRedirect: noRedirect},
		http: &http.Client{CheckRedirect: noRedirect},
	}
}

type reply struct {
	code   int
	header http.Header
	body   []byte
}

func (r reply) json(t *testing.T, v any) {
	t.Helper()
	if err := json.Unmarshal(r.body, v); err != nil {
		t.Fatalf("respuesta %d no es JSON: %v\n%s", r.code, err, r.body)
	}
}

func (s *liveServer) do(t *testing.T, c *http.Client, method, path string, body io.Reader, header map[string]string) reply {
	t.Helper()
	target := path
	if strings.HasPrefix(path, "/") {
		target = s.base + path
	}
	req, err := http.NewRequest(method, target, body)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	res, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	b, _ := io.ReadAll(res.Body)
	return reply{res.StatusCode, res.Header, b}
}

func (s *liveServer) form(t *testing.T, path string, v url.Values) reply {
	t.Helper()
	return s.do(t, s.http, http.MethodPost, path, strings.NewReader(v.Encode()),
		map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
}

func (s *liveServer) devLogin(t *testing.T) {
	t.Helper()
	if r := s.do(t, s.web, http.MethodGet, "/auth/dev/login?key="+s.devKey, nil, nil); r.code != http.StatusFound {
		t.Fatalf("login de desarrollo = %d", r.code)
	}
}

func (s *liveServer) register(t *testing.T, name, redirect string) string {
	t.Helper()
	r := s.do(t, s.http, http.MethodPost, "/oauth/register",
		strings.NewReader(`{"client_name":"`+name+`","redirect_uris":["`+redirect+`"],"grant_types":["authorization_code","refresh_token"],"token_endpoint_auth_method":"none"}`),
		map[string]string{"Content-Type": "application/json"})
	if r.code != http.StatusCreated {
		t.Fatalf("registro = %d %s", r.code, r.body)
	}
	var out struct {
		ClientID string `json:"client_id"`
		Method   string `json:"token_endpoint_auth_method"`
	}
	r.json(t, &out)
	if out.ClientID == "" || out.Method != "none" {
		t.Fatalf("registro = %s", r.body)
	}
	return out.ClientID
}

func challenge(verifier string) string {
	h := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(h[:])
}

type tokens struct {
	Access  string `json:"access_token"`
	Type    string `json:"token_type"`
	Expires int    `json:"expires_in"`
	Refresh string `json:"refresh_token"`
	Scope   string `json:"scope"`
	Error   string `json:"error"`
}

// authorize hace el recorrido del navegador: la autorización, la pantalla de
// consentimiento del panel con la sesión y la vuelta con el código.
func (s *liveServer) authorize(t *testing.T, clientID, redirect, verifier string, grant []string) string {
	t.Helper()
	q := url.Values{
		"response_type": {"code"}, "client_id": {clientID}, "redirect_uri": {redirect}, "scope": {"read write"},
		"state": {"estado-1"}, "code_challenge": {challenge(verifier)}, "code_challenge_method": {"S256"},
		"resource": {s.base + "/mcp"},
	}
	r := s.do(t, s.web, http.MethodGet, "/oauth/authorize?"+q.Encode(), nil, nil)
	loc := r.header.Get("Location")
	if r.code != http.StatusFound || !strings.HasPrefix(loc, s.base+"/authorize?request=") {
		t.Fatalf("autorizar = %d %q %s", r.code, loc, r.body)
	}
	id := strings.TrimPrefix(loc, s.base+"/authorize?request=")

	r = s.do(t, s.web, http.MethodGet, "/api/0/oauth/requests/"+id, nil, nil)
	var pending struct {
		ClientName  string   `json:"client_name"`
		RedirectURI string   `json:"redirect_uri"`
		Scopes      []string `json:"scopes"`
	}
	r.json(t, &pending)
	if r.code != http.StatusOK || pending.RedirectURI != redirect || !slices.Equal(pending.Scopes, []string{"read", "write"}) {
		t.Fatalf("petición pendiente = %d %s", r.code, r.body)
	}
	body, _ := json.Marshal(map[string]any{"scopes": grant})
	if r := s.do(t, s.web, http.MethodPost, "/api/0/oauth/requests/"+id+"/approve", bytes.NewReader(body),
		map[string]string{"Content-Type": "application/json"}); r.code != http.StatusForbidden {
		t.Fatalf("consentir sin Origin = %d, quiero 403", r.code)
	}
	r = s.do(t, s.web, http.MethodPost, "/api/0/oauth/requests/"+id+"/approve", bytes.NewReader(body),
		map[string]string{"Content-Type": "application/json", "Origin": s.base})
	var approved struct {
		RedirectTo string `json:"redirect_to"`
	}
	r.json(t, &approved)
	back, err := url.Parse(approved.RedirectTo)
	if r.code != http.StatusOK || err != nil || !strings.HasPrefix(approved.RedirectTo, redirect+"?") {
		t.Fatalf("consentir = %d %s", r.code, r.body)
	}
	if back.Query().Get("state") != "estado-1" || back.Query().Get("iss") != s.base || back.Query().Get("code") == "" {
		t.Fatalf("vuelta = %s", approved.RedirectTo)
	}
	return back.Query().Get("code")
}

func (s *liveServer) exchange(t *testing.T, clientID, redirect, code, verifier string) (tokens, int) {
	t.Helper()
	r := s.form(t, "/oauth/token", url.Values{
		"grant_type": {"authorization_code"}, "client_id": {clientID}, "redirect_uri": {redirect},
		"code": {code}, "code_verifier": {verifier}, "resource": {s.base + "/mcp"},
	})
	var tok tokens
	r.json(t, &tok)
	if r.header.Get("Cache-Control") != "no-store" {
		t.Errorf("token sin Cache-Control: no-store")
	}
	return tok, r.code
}

func (s *liveServer) refresh(t *testing.T, clientID, refresh string) (tokens, int) {
	t.Helper()
	r := s.form(t, "/oauth/token", url.Values{"grant_type": {"refresh_token"}, "client_id": {clientID}, "refresh_token": {refresh}})
	var tok tokens
	r.json(t, &tok)
	return tok, r.code
}

type bearerTransport struct{ token string }

func (b bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

func (s *liveServer) mcp(t *testing.T, token string) (*mcp.ClientSession, error) {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "prueba", Version: "0"}, nil)
	return client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint: s.base + "/mcp", HTTPClient: &http.Client{Transport: bearerTransport{token}}, MaxRetries: -1,
	}, nil)
}

func toolNames(t *testing.T, cs *mcp.ClientSession) []string {
	t.Helper()
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
	}
	return names
}

func callTool(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	var out strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			out.WriteString(tc.Text)
		}
	}
	return out.String(), res.IsError
}

// mcpStatus es el código HTTP de un initialize a /mcp con ese token.
func (s *liveServer) mcpStatus(t *testing.T, token string) reply {
	t.Helper()
	h := map[string]string{"Content-Type": "application/json", "Accept": "application/json, text/event-stream"}
	if token != "" {
		h["Authorization"] = "Bearer " + token
	}
	return s.do(t, s.http, http.MethodPost, "/mcp", strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"x","version":"0"}}}`), h)
}

func TestMCPOAuthEndToEnd(t *testing.T) {
	s := startServer(t)
	ctx := context.Background()
	p := s.db.AddProject(t, "backend")
	issue := s.db.AddIssue(t, storetest.Issue{ProjectID: p.ID, Title: "ZeroDivisionError: division by zero", Count: 1})
	_, apiToken, err := s.db.CreateToken(ctx, "agente")
	if err != nil {
		t.Fatal(err)
	}

	// 401 con la metadata del recurso.
	r := s.mcpStatus(t, "")
	meta := s.base + "/.well-known/oauth-protected-resource/mcp"
	if r.code != http.StatusUnauthorized || !strings.Contains(r.header.Get("WWW-Authenticate"), `resource_metadata="`+meta+`"`) {
		t.Fatalf("/mcp sin credenciales = %d %q", r.code, r.header.Get("WWW-Authenticate"))
	}

	// Descubrimiento.
	for _, path := range []string{"/.well-known/oauth-protected-resource/mcp", "/.well-known/oauth-protected-resource"} {
		var prm struct {
			Resource string   `json:"resource"`
			Servers  []string `json:"authorization_servers"`
			Scopes   []string `json:"scopes_supported"`
		}
		r = s.do(t, s.http, http.MethodGet, path, nil, nil)
		r.json(t, &prm)
		if r.code != http.StatusOK || prm.Resource != s.base+"/mcp" || !slices.Equal(prm.Servers, []string{s.base}) ||
			!slices.Equal(prm.Scopes, []string{"read", "write"}) {
			t.Fatalf("%s = %d %s", path, r.code, r.body)
		}
	}
	var asm map[string]any
	r = s.do(t, s.http, http.MethodGet, "/.well-known/oauth-authorization-server", nil, nil)
	r.json(t, &asm)
	if asm["issuer"] != s.base || asm["authorization_endpoint"] != s.base+"/oauth/authorize" ||
		asm["token_endpoint"] != s.base+"/oauth/token" || asm["registration_endpoint"] != s.base+"/oauth/register" ||
		asm["revocation_endpoint"] != s.base+"/oauth/revoke" || asm["client_id_metadata_document_supported"] != true ||
		!slices.Equal(asm["code_challenge_methods_supported"].([]any), []any{"S256"}) {
		t.Fatalf("metadata del servidor de autorización = %s", r.body)
	}

	// Registro, consentimiento solo con read y canje con PKCE.
	redirect := "http://127.0.0.1:43123/callback"
	clientID := s.register(t, "Cliente de prueba", redirect)
	s.devLogin(t)
	verifier := strings.Repeat("v", 20) + storetest.RandomHex(t, 16)
	code := s.authorize(t, clientID, redirect, verifier, []string{"read"})
	tok, status := s.exchange(t, clientID, redirect, code, verifier)
	if status != http.StatusOK || tok.Type != "Bearer" || tok.Scope != "read" || tok.Expires != 3600 ||
		!strings.HasPrefix(tok.Access, "mlo_") || !strings.HasPrefix(tok.Refresh, "mlr_") {
		t.Fatalf("canje = %d %+v", status, tok)
	}

	// El MCP con read: sin las herramientas de escribir.
	cs, err := s.mcp(t, tok.Access)
	if err != nil {
		t.Fatal(err)
	}
	names := toolNames(t, cs)
	if !slices.Contains(names, "list_issues") || !slices.Contains(names, "get_issue_context") {
		t.Fatalf("herramientas = %v", names)
	}
	for _, w := range []string{"add_comment", "assign_issue", "set_issue_status"} {
		if slices.Contains(names, w) {
			t.Errorf("con read sale %s", w)
		}
	}
	if out, isErr := callTool(t, cs, "list_issues", nil); isErr || !strings.Contains(out, "ZeroDivisionError") {
		t.Fatalf("list_issues = %v %s", isErr, out)
	}
	out, isErr := callTool(t, cs, "add_comment", map[string]any{"issue_id": issue, "body": "no debería quedar"})
	if !isErr || !strings.Contains(out, `"write"`) {
		t.Fatalf("add_comment con read = %v %s", isErr, out)
	}
	_ = cs.Close()

	// El token de OAuth solo vale para /mcp.
	if r := s.do(t, s.http, http.MethodGet, "/api/0/issues/", nil, map[string]string{"Authorization": "Bearer " + tok.Access}); r.code != http.StatusUnauthorized {
		t.Errorf("/api/0/issues/ con el token de OAuth = %d, quiero 401", r.code)
	}

	// Refresco con rotación.
	tok2, status := s.refresh(t, clientID, tok.Refresh)
	if status != http.StatusOK || tok2.Refresh == tok.Refresh || tok2.Access == tok.Access || tok2.Scope != "read" {
		t.Fatalf("refresco = %d %+v", status, tok2)
	}
	if r := s.mcpStatus(t, tok2.Access); r.code != http.StatusOK {
		t.Fatalf("/mcp con el acceso refrescado = %d %s", r.code, r.body)
	}
	// Reusar el refresco gastado revoca la concesión entera.
	if bad, status := s.refresh(t, clientID, tok.Refresh); status != http.StatusBadRequest || bad.Error != "invalid_grant" {
		t.Fatalf("reuso del refresco = %d %+v", status, bad)
	}
	r = s.mcpStatus(t, tok2.Access)
	if r.code != http.StatusUnauthorized || !strings.Contains(r.header.Get("WWW-Authenticate"), `error="invalid_token"`) {
		t.Fatalf("/mcp tras el reuso = %d %q", r.code, r.header.Get("WWW-Authenticate"))
	}
	if bad, status := s.refresh(t, clientID, tok2.Refresh); status != http.StatusBadRequest || bad.Error != "invalid_grant" {
		t.Fatalf("refresco de una concesión revocada = %d %+v", status, bad)
	}

	// Otra conexión, con write: lo que hace queda como «dev vía el cliente».
	clientID2 := s.register(t, "Cliente con escritura", redirect)
	verifier2 := storetest.RandomHex(t, 32)
	tok3, status := s.exchange(t, clientID2, redirect, s.authorize(t, clientID2, redirect, verifier2, []string{"read", "write"}), verifier2)
	if status != http.StatusOK || tok3.Scope != "read write" {
		t.Fatalf("canje con write = %d %+v", status, tok3)
	}
	cs, err = s.mcp(t, tok3.Access)
	if err != nil {
		t.Fatal(err)
	}
	if out, isErr := callTool(t, cs, "add_comment", map[string]any{"issue_id": issue, "body": "comentado con OAuth"}); isErr {
		t.Fatalf("add_comment con write = %s", out)
	}
	if out, isErr := callTool(t, cs, "assign_issue", map[string]any{"issue_id": issue, "assignee": "me"}); isErr || !strings.Contains(out, `"dev"`) {
		t.Fatalf("assign_issue me = %v %s", isErr, out)
	}
	_ = cs.Close()
	var hist struct {
		Data []store.HistoryEntry `json:"data"`
	}
	r = s.do(t, s.http, http.MethodGet, "/api/0/issues/"+strconv.FormatInt(issue, 10)+"/history/", nil, map[string]string{"Authorization": "Bearer " + apiToken})
	r.json(t, &hist)
	if len(hist.Data) != 2 || hist.Data[0].Author == nil || *hist.Data[0].Author != (store.Actor{Name: "dev", Kind: "user", Via: "Cliente con escritura"}) ||
		hist.Data[1].Author == nil || hist.Data[1].Author.Via != "Cliente con escritura" || hist.Data[1].Assignee.Via != "" {
		t.Fatalf("historial = %s", r.body)
	}

	// Aplicaciones conectadas: solo la viva, y revocación por RFC 7009.
	var grants struct {
		Data []store.OAuthGrant `json:"data"`
	}
	r = s.do(t, s.web, http.MethodGet, "/api/0/oauth/grants/", nil, nil)
	r.json(t, &grants)
	if r.code != http.StatusOK || len(grants.Data) != 1 || grants.Data[0].ClientName != "Cliente con escritura" ||
		!slices.Equal(grants.Data[0].Scopes, []string{"read", "write"}) || grants.Data[0].LastUsedAt == nil ||
		grants.Data[0].LastUsedIP == nil || *grants.Data[0].LastUsedIP != "127.0.0.1" {
		t.Fatalf("aplicaciones conectadas = %d %s", r.code, r.body)
	}
	if r := s.do(t, s.http, http.MethodGet, "/api/0/oauth/grants/", nil, map[string]string{"Authorization": "Bearer " + apiToken}); r.code != http.StatusUnauthorized {
		t.Errorf("aplicaciones conectadas con un token ie_ = %d, quiero 401", r.code)
	}
	if r := s.form(t, "/oauth/revoke", url.Values{"token": {tok3.Refresh}, "client_id": {clientID2}, "token_type_hint": {"refresh_token"}}); r.code != http.StatusOK {
		t.Fatalf("revocar = %d %s", r.code, r.body)
	}
	if r := s.mcpStatus(t, tok3.Access); r.code != http.StatusUnauthorized {
		t.Fatalf("/mcp tras revocar = %d", r.code)
	}
	if bad, status := s.refresh(t, clientID2, tok3.Refresh); status != http.StatusBadRequest || bad.Error != "invalid_grant" {
		t.Fatalf("refresco tras revocar = %d %+v", status, bad)
	}
	r = s.do(t, s.web, http.MethodGet, "/api/0/oauth/grants/", nil, nil)
	r.json(t, &grants)
	if len(grants.Data) != 0 {
		t.Fatalf("tras revocar quedan %s", r.body)
	}

	// Los tokens ie_ siguen como antes, con todas las herramientas.
	cs, err = s.mcp(t, apiToken)
	if err != nil {
		t.Fatal(err)
	}
	if names := toolNames(t, cs); !slices.Contains(names, "add_comment") || !slices.Contains(names, "set_issue_status") {
		t.Fatalf("herramientas con ie_ = %v", names)
	}
	_ = cs.Close()
}

func TestMCPOAuthRevokeFromPanelAndPeople(t *testing.T) {
	s := startServer(t)
	ctx := context.Background()
	redirect := "https://cliente.example.com/vuelta"
	s.devLogin(t)
	connect := func(name string) tokens {
		id := s.register(t, name, redirect)
		v := storetest.RandomHex(t, 32)
		tok, status := s.exchange(t, id, redirect, s.authorize(t, id, redirect, v, []string{"read"}), v)
		if status != http.StatusOK {
			t.Fatalf("canje = %d %+v", status, tok)
		}
		return tok
	}
	a, b := connect("Uno"), connect("Dos")
	var grants struct {
		Data []store.OAuthGrant `json:"data"`
	}
	r := s.do(t, s.web, http.MethodGet, "/api/0/oauth/grants/", nil, nil)
	r.json(t, &grants)
	if len(grants.Data) != 2 || grants.Data[0].ClientName != "Dos" {
		t.Fatalf("aplicaciones = %s", r.body)
	}
	path := "/api/0/oauth/grants/" + strconv.FormatInt(grants.Data[1].ID, 10)
	if r := s.do(t, s.web, http.MethodDelete, path, nil, nil); r.code != http.StatusForbidden {
		t.Fatalf("revocar sin Origin = %d, quiero 403", r.code)
	}
	if r := s.do(t, s.web, http.MethodDelete, path, nil, map[string]string{"Origin": s.base}); r.code != http.StatusNoContent {
		t.Fatalf("revocar en el panel = %d %s", r.code, r.body)
	}
	if r := s.mcpStatus(t, a.Access); r.code != http.StatusUnauthorized {
		t.Fatalf("/mcp con la revocada = %d", r.code)
	}
	if r := s.mcpStatus(t, b.Access); r.code != http.StatusOK {
		t.Fatalf("/mcp con la otra = %d", r.code)
	}

	var out bytes.Buffer
	if err := peopleCommand(ctx, s.db.Store, []string{"revoke", "dev"}, &out, time.Now); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "MCP OAuth grants revoked: 1") {
		t.Errorf("people revoke = %q", out.String())
	}
	if r := s.mcpStatus(t, b.Access); r.code != http.StatusUnauthorized {
		t.Fatalf("/mcp tras people revoke = %d", r.code)
	}
}

// Una conexión de OAuth autorizada antes del nombre Mothlog (cliente iec_,
// acceso ieo_ y refresco ier_) sigue valiendo sin volver a autorizar: el
// acceso entra en el MCP y el refresco da tokens nuevos, ya con mlo_ y mlr_.
// Las filas son las que dejaba el código de antes: solo el sha256 de cada token.
func TestMCPOAuthTokensFromBeforeRename(t *testing.T) {
	s := startServer(t)
	ctx := context.Background()
	s.devLogin(t)
	p := s.db.AddProject(t, "backend")
	s.db.AddIssue(t, storetest.Issue{ProjectID: p.ID, Title: "ZeroDivisionError: division by zero", Count: 1})

	const clientID = "iec_cliente-de-antes"
	access, refresh := "ieo_"+storetest.RandomHex(t, 32), "ier_"+storetest.RandomHex(t, 32)
	now := time.Now().UTC()
	if err := s.db.CreateOAuthClient(ctx, store.OAuthClient{ID: clientID, Name: "Cliente de antes",
		RedirectURIs: []string{"http://127.0.0.1:9/cb"}}, now); err != nil {
		t.Fatal(err)
	}
	var grant int64
	if err := s.db.Pool().QueryRow(ctx, `INSERT INTO oauth_grants (login, client_id, client_name, redirect_uri, scopes, resource, created_at)
		VALUES ('dev', $1, 'Cliente de antes', 'http://127.0.0.1:9/cb', 'read', $2, $3) RETURNING id`,
		clientID, s.base+"/mcp", now).Scan(&grant); err != nil {
		t.Fatal(err)
	}
	for kind, secret := range map[string]string{"access": access, "refresh": refresh} {
		sum := sha256.Sum256([]byte(secret))
		if _, err := s.db.Pool().Exec(ctx, `INSERT INTO oauth_tokens (token_sha256, grant_id, kind, scopes, created_at, expires_at)
			VALUES ($1, $2, $3, 'read', $4, $5)`, sum[:], grant, kind, now, now.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}

	cs, err := s.mcp(t, access)
	if err != nil {
		t.Fatalf("MCP con el acceso ieo_: %v", err)
	}
	if out, isErr := callTool(t, cs, "list_issues", nil); isErr || !strings.Contains(out, "ZeroDivisionError") {
		t.Errorf("list_issues con el acceso ieo_ = %v %s", isErr, out)
	}
	_ = cs.Close()

	tok, status := s.refresh(t, clientID, refresh)
	if status != http.StatusOK || !strings.HasPrefix(tok.Access, "mlo_") || !strings.HasPrefix(tok.Refresh, "mlr_") || tok.Scope != "read" {
		t.Fatalf("refresco con ier_ = %d %+v", status, tok)
	}
	if r := s.mcpStatus(t, tok.Access); r.code != http.StatusOK {
		t.Fatalf("/mcp con el acceso refrescado = %d %s", r.code, r.body)
	}
	if bad, status := s.refresh(t, clientID, refresh); status != http.StatusBadRequest || bad.Error != "invalid_grant" {
		t.Errorf("el refresco ier_ gastado sigue valiendo: %d %+v", status, bad)
	}
}
