package webauth

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/intake-es/mothlog/internal/store"
)

// fakeOIDC es un proveedor OIDC local: descubrimiento, JWKS, token y
// userinfo. authorize lo hace el test con approve, que elige quién entra.
type fakeOIDC struct {
	*httptest.Server
	t *testing.T

	mu         sync.Mutex
	rsaKey     *rsa.PrivateKey
	kid        string
	codes      map[string]fakeGrant
	tokenCalls int
	jwksCalls  int
	userinfo   map[string]any
	lastBasic  bool
}

type fakeGrant struct {
	challenge, nonce, redirect string
	claims                     map[string]any
	// sign cambia cómo se firma el id_token; nil es la clave buena.
	sign func(header, payload string) string
}

func newFakeOIDC(t *testing.T) *fakeOIDC {
	t.Helper()
	f := &fakeOIDC{t: t, codes: map[string]fakeGrant{}, kid: "k1", rsaKey: newRSAKey(t)}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                f.URL,
			"authorization_endpoint":                f.URL + "/authorize",
			"token_endpoint":                        f.URL + "/token",
			"jwks_uri":                              f.URL + "/jwks",
			"userinfo_endpoint":                     f.URL + "/userinfo",
			"code_challenge_methods_supported":      []string{"S256"},
			"token_endpoint_auth_methods_supported": []string{"client_secret_basic"},
		})
	})
	mux.HandleFunc("GET /jwks", func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.jwksCalls++
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{rsaJWK(f.kid, &f.rsaKey.PublicKey)}})
	})
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		f.mu.Lock()
		defer f.mu.Unlock()
		f.tokenCalls++
		id, secret, basic := r.BasicAuth()
		f.lastBasic = basic
		g, ok := f.codes[r.PostForm.Get("code")]
		delete(f.codes, r.PostForm.Get("code"))
		sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
		if !ok || id != "cliente-oidc" || secret != "secreto-oidc" || r.PostForm.Get("grant_type") != "authorization_code" ||
			r.PostForm.Get("redirect_uri") != g.redirect || base64.RawURLEncoding.EncodeToString(sum[:]) != g.challenge {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
			return
		}
		claims := map[string]any{"iss": f.URL, "aud": "cliente-oidc", "nonce": g.nonce,
			"iat": float64(testNow.Unix()), "exp": float64(testNow.Add(5 * time.Minute).Unix())}
		for k, v := range g.claims {
			if v == nil {
				delete(claims, k)
			} else {
				claims[k] = v
			}
		}
		header := map[string]any{"alg": "RS256", "kid": f.kid, "typ": "JWT"}
		h, p := segment(t, header), segment(t, claims)
		sig := f.signRS256(h + "." + p)
		if g.sign != nil {
			sig = g.sign(h, p)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id_token": h + "." + p + "." + sig, "access_token": "at-" + claims["sub"].(string), "token_type": "Bearer"})
	})
	mux.HandleFunc("GET /userinfo", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		sub, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer at-")
		if !ok {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		// Sin userinfo preparado, solo el sub de quien pregunta.
		info := map[string]any{"sub": sub}
		if f.userinfo != nil {
			info = f.userinfo
		}
		_ = json.NewEncoder(w).Encode(info)
	})
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	return f
}

var testNow = time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)

func newRSAKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func rsaJWK(kid string, pub *rsa.PublicKey) map[string]any {
	return map[string]any{"kty": "RSA", "kid": kid, "use": "sig", "alg": "RS256",
		"n": base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
		"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes())}
}

func segment(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func signRS256(key *rsa.PrivateKey, signed string) string {
	sum := sha256.Sum256([]byte(signed))
	sig, _ := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	return base64.RawURLEncoding.EncodeToString(sig)
}

func (f *fakeOIDC) signRS256(signed string) string { return signRS256(f.rsaKey, signed) }

// rotate cambia la clave y su kid, como al rotar las claves del emisor.
func (f *fakeOIDC) rotate(kid string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rsaKey, f.kid = newRSAKey(f.t), kid
}

func (f *fakeOIDC) counts() (token, jwks int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tokenCalls, f.jwksCalls
}

func oidcConfig(f *fakeOIDC) Config {
	return Config{
		SessionSecret: testSecret,
		PublicURL:     publicURL,
		ListenAddr:    ":8080",
		OIDC: OIDCConfig{
			Issuer:         f.URL,
			ClientID:       "cliente-oidc",
			ClientSecret:   "secreto-oidc",
			Name:           "Acme SSO",
			AllowedDomains: []string{"example.com"},
		},
	}
}

func user(sub, email string) map[string]any {
	return map[string]any{"sub": sub, "email": email, "email_verified": true, "name": "Persona " + sub}
}

// oidcStart pide /auth/oidc/login y devuelve la URL de authorize y la cookie.
func (h *harness) oidcStart(t *testing.T, next string) (url.Values, *http.Cookie) {
	t.Helper()
	res := h.do(t, http.MethodGet, "/auth/oidc/login?next="+url.QueryEscape(next), nil, nil)
	loc, _ := url.Parse(res.Header.Get("Location"))
	sc := cookieNamed(res, oidcStateCookie)
	if res.StatusCode != http.StatusFound || sc == nil || loc.Path != "/authorize" {
		t.Fatalf("login oidc = %d %s", res.StatusCode, loc)
	}
	return loc.Query(), sc
}

// approve hace de la pantalla del proveedor: apunta el código con lo que
// pidió /authorize y devuelve la query del callback.
func (f *fakeOIDC) approve(q url.Values, g fakeGrant) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	code := "code-" + randomHex(8)
	g.challenge, g.redirect = q.Get("code_challenge"), q.Get("redirect_uri")
	if g.nonce == "" {
		g.nonce = q.Get("nonce")
	}
	f.codes[code] = g
	return "code=" + code + "&state=" + url.QueryEscape(q.Get("state")) + "&iss=" + url.QueryEscape(f.URL)
}

func (h *harness) oidcLogin(t *testing.T, f *fakeOIDC, g fakeGrant) *http.Response {
	t.Helper()
	q, sc := h.oidcStart(t, "/")
	return h.do(t, http.MethodGet, "/auth/oidc/callback?"+f.approve(q, g), []*http.Cookie{sc}, nil)
}

func TestOIDCLogin(t *testing.T) {
	f := newFakeOIDC(t)
	h := newHarness(t, oidcConfig(f))
	people := &savedPeople{}
	h.auth.SavePeople(people)

	q, sc := h.oidcStart(t, "/issues/42")
	if q.Get("response_type") != "code" || q.Get("client_id") != "cliente-oidc" || q.Get("scope") != "openid email profile" ||
		q.Get("redirect_uri") != publicURL+"/auth/oidc/callback" || q.Get("code_challenge_method") != "S256" ||
		len(q.Get("code_challenge")) != 43 || len(q.Get("state")) != 64 || len(q.Get("nonce")) != 64 {
		t.Fatalf("authorize = %v", q)
	}
	if !sc.HttpOnly || !sc.Secure || sc.SameSite != http.SameSiteLaxMode || sc.Path != "/auth/oidc/" || sc.MaxAge != 600 {
		t.Errorf("cookie de state = %+v", sc)
	}
	res := h.do(t, http.MethodGet, "/auth/oidc/callback?"+f.approve(q, fakeGrant{claims: user("u-1", "Ana@Example.com")}), []*http.Cookie{sc}, nil)
	if res.StatusCode != http.StatusFound || res.Header.Get("Location") != "/issues/42" {
		t.Fatalf("callback = %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	if gone := cookieNamed(res, oidcStateCookie); gone == nil || gone.MaxAge >= 0 {
		t.Errorf("la cookie de state no se borra: %+v", gone)
	}
	s := cookieNamed(res, sessionCookie)
	if s == nil {
		t.Fatal("sin sesión")
	}
	res = h.do(t, http.MethodGet, "/api/0/issues/", []*http.Cookie{s}, nil)
	if b, _ := io.ReadAll(res.Body); string(b) != "session:Ana@Example.com" {
		t.Errorf("con la sesión = %d %s", res.StatusCode, b)
	}
	if len(people.got) != 1 || people.got[0] != (store.Person{Login: "Ana@Example.com", Name: "Persona u-1", Provider: "oidc", Subject: "u-1"}) {
		t.Errorf("guardada = %+v", people.got)
	}
	if _, _ = f.counts(); !f.lastBasic {
		t.Error("el secreto no va por HTTP Basic, como anuncia el emisor")
	}

	// Sin correo, el preferred_username con el prefijo oidc:; el login de la
	// sesión es el que da la base.
	people.login = func(p store.Person) string { return p.Login + "~2" }
	h.auth.oidc.cfg.AllowedDomains = nil
	h.auth.oidc.cfg.AllowAny = true
	res = h.oidcLogin(t, f, fakeGrant{claims: map[string]any{"sub": "u-2", "preferred_username": "luis"}})
	res = h.do(t, http.MethodGet, "/api/0/issues/", []*http.Cookie{cookieNamed(res, sessionCookie)}, nil)
	if b, _ := io.ReadAll(res.Body); string(b) != "session:oidc:luis~2" {
		t.Errorf("con preferred_username = %s", b)
	}

	// Un correo sin verificar no sirve de login: el preferred_username o, sin
	// él, el sub, con el prefijo. Así nadie se queda con el correo o el login
	// de GitHub de otra persona antes de que esta entre.
	people.login = nil
	for _, tc := range []struct {
		claims map[string]any
		want   string
	}{
		{map[string]any{"sub": "u-3", "email": "jefa@example.com", "email_verified": false, "preferred_username": "eve"}, "oidc:eve"},
		{map[string]any{"sub": "u-4", "email": "jefa@example.com"}, "oidc:u-4"},
		{map[string]any{"sub": "u-6", "email": "jefa@example.com", "email_verified": false, "preferred_username": "jefa@example.com"}, "oidc:jefa@example.com"},
		{map[string]any{"sub": "u-7", "preferred_username": "ana"}, "oidc:ana"},
		{map[string]any{"sub": "u-5", "email": "jefa@example.com", "email_verified": "true"}, "jefa@example.com"},
	} {
		res = h.oidcLogin(t, f, fakeGrant{claims: tc.claims})
		res = h.do(t, http.MethodGet, "/api/0/issues/", []*http.Cookie{cookieNamed(res, sessionCookie)}, nil)
		if b, _ := io.ReadAll(res.Body); string(b) != "session:"+tc.want {
			t.Errorf("%v = %s; quiero %s", tc.claims, b, tc.want)
		}
	}
}

func TestOIDCRejected(t *testing.T) {
	f := newFakeOIDC(t)
	h := newHarness(t, oidcConfig(f))
	people := &savedPeople{}
	h.auth.SavePeople(people)
	other := newRSAKey(t)

	for _, tc := range []struct {
		name   string
		grant  fakeGrant
		reason string
	}{
		{"otro dominio", fakeGrant{claims: user("u-3", "eve@evil.example")}, "domain"},
		{"subdominio", fakeGrant{claims: user("u-3", "eve@mail.example.com")}, "domain"},
		{"dominio parecido", fakeGrant{claims: user("u-3", "eve@notexample.com")}, "domain"},
		{"correo sin verificar", fakeGrant{claims: map[string]any{"sub": "u-3", "email": "eve@example.com", "email_verified": false}}, "domain"},
		{"sin correo", fakeGrant{claims: map[string]any{"sub": "u-3"}}, "domain"},
		{"firma mala", fakeGrant{claims: user("u-1", "ana@example.com"), sign: func(h, p string) string { return signRS256(other, h+"."+p) }}, "oidc"},
		{"payload cambiado", fakeGrant{claims: user("u-1", "ana@example.com"), sign: func(h, _ string) string {
			return f.signRS256(h + "." + segment(t, user("u-1", "otra@example.com")))
		}}, "oidc"},
		{"sin firma", fakeGrant{claims: user("u-1", "ana@example.com"), sign: func(string, string) string { return "" }}, "oidc"},
		{"caducado", fakeGrant{claims: map[string]any{"sub": "u-1", "email": "ana@example.com", "email_verified": true,
			"exp": float64(testNow.Add(-2 * time.Minute).Unix())}}, "oidc"},
		{"sin exp", fakeGrant{claims: map[string]any{"sub": "u-1", "email": "ana@example.com", "email_verified": true, "exp": nil}}, "oidc"},
		{"nonce que no cuadra", fakeGrant{claims: user("u-1", "ana@example.com"), nonce: "otro-nonce"}, "oidc"},
		{"sin nonce", fakeGrant{claims: map[string]any{"sub": "u-1", "email": "ana@example.com", "email_verified": true, "nonce": nil}}, "oidc"},
		{"otro emisor", fakeGrant{claims: map[string]any{"sub": "u-1", "email": "ana@example.com", "email_verified": true, "iss": "https://evil.example"}}, "oidc"},
		{"para otro cliente", fakeGrant{claims: map[string]any{"sub": "u-1", "email": "ana@example.com", "email_verified": true, "aud": "otro"}}, "oidc"},
		{"varias aud sin azp", fakeGrant{claims: map[string]any{"sub": "u-1", "email": "ana@example.com", "email_verified": true, "aud": []any{"cliente-oidc", "otro"}}}, "oidc"},
		{"sin sub", fakeGrant{claims: map[string]any{"sub": "", "email": "ana@example.com", "email_verified": true}}, "oidc"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := h.oidcLogin(t, f, tc.grant)
			if res.StatusCode != http.StatusFound || res.Header.Get("Location") != "/login?error="+tc.reason || cookieNamed(res, sessionCookie) != nil {
				t.Errorf("= %d %s", res.StatusCode, res.Header.Get("Location"))
			}
		})
	}
	if len(people.got) != 0 {
		t.Errorf("se guardaron rechazados: %+v", people.got)
	}

	// alg none o HS256 con el módulo como secreto no valen.
	for _, alg := range []string{"none", "HS256"} {
		k := jwk{kid: "k1", key: &f.rsaKey.PublicKey}
		if k.verify(alg, []byte("a.b"), nil) == nil {
			t.Errorf("alg %s vale", alg)
		}
	}
}

func TestOIDCBadState(t *testing.T) {
	f := newFakeOIDC(t)
	h := newHarness(t, oidcConfig(f))
	q, sc := h.oidcStart(t, "/")
	q2, _ := h.oidcStart(t, "/")
	good := f.approve(q, fakeGrant{claims: user("u-1", "ana@example.com")})
	changed := strings.Replace(good, "state="+q.Get("state"), "state="+q2.Get("state"), 1)
	forged := *sc
	payload, _, _ := strings.Cut(sc.Value, ".")
	forged.Value = payload + ".firmafalsa"

	for _, tc := range []struct {
		name   string
		query  string
		cookie *http.Cookie
		later  time.Duration
		reason string
	}{
		{"state cambiado", changed, sc, 0, "state"},
		{"sin cookie", good, nil, 0, "state"},
		{"cookie con firma mala", good, &forged, 0, "state"},
		{"caducado", good, sc, 11 * time.Minute, "state"},
		{"cookie de GitHub", good, &http.Cookie{Name: oidcStateCookie, Value: mustSign(t, h.auth, "state", oauthState{State: q.Get("state"), Expires: testNow.Add(time.Hour).Unix()})}, 0, "state"},
		{"iss de otro emisor", strings.Replace(good, "iss="+url.QueryEscape(f.URL), "iss=https%3A%2F%2Fevil.example", 1), sc, 0, "oidc"},
		{"cancelado", "error=access_denied&state=" + q.Get("state"), sc, 0, "oidc_denied"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h.clock.now = testNow.Add(tc.later)
			var cookies []*http.Cookie
			if tc.cookie != nil {
				cookies = append(cookies, tc.cookie)
			}
			res := h.do(t, http.MethodGet, "/auth/oidc/callback?"+tc.query, cookies, nil)
			if res.StatusCode != http.StatusFound || res.Header.Get("Location") != "/login?error="+tc.reason || cookieNamed(res, sessionCookie) != nil {
				t.Errorf("= %d %s", res.StatusCode, res.Header.Get("Location"))
			}
		})
	}
	if n, _ := f.counts(); n != 0 {
		t.Errorf("con un state malo se canjeó el código %d veces", n)
	}
	// El código bueno con su cookie, después de todo eso, sigue valiendo.
	h.clock.now = testNow
	if res := h.do(t, http.MethodGet, "/auth/oidc/callback?"+good, []*http.Cookie{sc}, nil); cookieNamed(res, sessionCookie) == nil {
		t.Errorf("el bueno = %s", res.Header.Get("Location"))
	}
}

func mustSign(t *testing.T, a *Auth, purpose string, v any) string {
	t.Helper()
	s, err := a.sign(purpose, v)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestOIDCGroups(t *testing.T) {
	f := newFakeOIDC(t)
	cfg := oidcConfig(f)
	cfg.OIDC.AllowedDomains = nil
	cfg.OIDC.AllowedGroups = []string{"ingenieria", "soporte"}
	cfg.OIDC.GroupsClaim = "roles"
	h := newHarness(t, cfg)

	for _, tc := range []struct {
		name   string
		claims map[string]any
		reason string
	}{
		{"con el grupo", map[string]any{"sub": "u-1", "roles": []any{"ventas", "soporte"}}, ""},
		{"grupo como cadena", map[string]any{"sub": "u-1", "roles": "ingenieria"}, ""},
		{"sin el grupo", map[string]any{"sub": "u-2", "roles": []any{"ventas"}}, "group"},
		{"en otro claim", map[string]any{"sub": "u-2", "groups": []any{"soporte"}}, "group"},
		{"mayúsculas", map[string]any{"sub": "u-2", "roles": []any{"Soporte"}}, "group"},
	} {
		res := h.oidcLogin(t, f, fakeGrant{claims: tc.claims})
		want := "/"
		if tc.reason != "" {
			want = "/login?error=" + tc.reason
		}
		if res.Header.Get("Location") != want || (cookieNamed(res, sessionCookie) != nil) != (tc.reason == "") {
			t.Errorf("%s = %s", tc.name, res.Header.Get("Location"))
		}
	}

	// Si el id_token no trae los grupos, se piden a userinfo.
	f.mu.Lock()
	f.userinfo = map[string]any{"sub": "u-4", "roles": []any{"soporte"}}
	f.mu.Unlock()
	if res := h.oidcLogin(t, f, fakeGrant{claims: map[string]any{"sub": "u-4"}}); res.Header.Get("Location") != "/" {
		t.Errorf("con los grupos en userinfo = %s", res.Header.Get("Location"))
	}
	// userinfo de otro sub no vale.
	if res := h.oidcLogin(t, f, fakeGrant{claims: map[string]any{"sub": "u-5"}}); res.Header.Get("Location") != "/login?error=oidc" {
		t.Errorf("userinfo de otro = %s", res.Header.Get("Location"))
	}

	// Dominio y grupo a la vez: hay que cumplir los dos.
	h.auth.oidc.cfg.AllowedDomains = []string{"example.com"}
	f.mu.Lock()
	f.userinfo = nil
	f.mu.Unlock()
	for _, tc := range []struct {
		email, role, want string
	}{
		{"a@example.com", "soporte", "/"},
		{"a@example.com", "ventas", "/login?error=group"},
		{"a@evil.example", "soporte", "/login?error=domain"},
	} {
		claims := map[string]any{"sub": "u-1", "email": tc.email, "email_verified": true, "roles": []any{tc.role}}
		if res := h.oidcLogin(t, f, fakeGrant{claims: claims}); res.Header.Get("Location") != tc.want {
			t.Errorf("%s en %s = %s", tc.email, tc.role, res.Header.Get("Location"))
		}
	}
}

func TestOIDCKeyRotation(t *testing.T) {
	f := newFakeOIDC(t)
	h := newHarness(t, oidcConfig(f))
	login := func() string {
		return h.oidcLogin(t, f, fakeGrant{claims: user("u-1", "ana@example.com")}).Header.Get("Location")
	}
	for range 3 {
		if got := login(); got != "/" {
			t.Fatalf("login = %s", got)
		}
	}
	if _, jwks := f.counts(); jwks != 1 {
		t.Errorf("las claves se pidieron %d veces; con caché, 1", jwks)
	}

	// Rota la clave: un kid desconocido hace que se pidan otra vez, pero como
	// mucho una vez por minuto.
	f.rotate("k2")
	if got := login(); got != "/login?error=oidc" {
		t.Errorf("recién pedidas, un kid nuevo no las vuelve a pedir: %s", got)
	}
	h.clock.now = testNow.Add(2 * time.Minute)
	if got := login(); got != "/" {
		t.Errorf("tras rotar = %s", got)
	}
	if _, jwks := f.counts(); jwks != 2 {
		t.Errorf("las claves se pidieron %d veces; quiero 2", jwks)
	}
	h.clock.now = testNow
}

func TestOIDCECKey(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	x, y := make([]byte, 32), make([]byte, 32)
	key.X.FillBytes(x)
	key.Y.FillBytes(y)
	raw, _ := json.Marshal(map[string]string{"kty": "EC", "crv": "P-256", "kid": "e1",
		"x": base64.RawURLEncoding.EncodeToString(x), "y": base64.RawURLEncoding.EncodeToString(y)})
	k, ok := parseJWK(raw)
	if !ok {
		t.Fatal("no lee la clave EC")
	}
	signed := []byte("cabecera.payload")
	sum := sha256.Sum256(signed)
	r, s, err := ecdsa.Sign(rand.Reader, key, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	if err := k.verify("ES256", signed, sig); err != nil {
		t.Errorf("ES256: %v", err)
	}
	if k.verify("ES256", []byte("otra.cosa"), sig) == nil || k.verify("RS256", signed, sig) == nil || k.verify("ES384", signed, sig) == nil {
		t.Error("vale una firma EC que no debería")
	}
	for _, bad := range []string{
		`{"kty":"EC","crv":"P-256","x":"AAAA","y":"AAAA"}`,
		`{"kty":"RSA","n":"AQAB","e":"AQAB"}`,
		`{"kty":"oct","k":"c2VjcmV0"}`,
		`{"kty":"RSA","use":"enc","n":"` + strings.Repeat("A", 344) + `","e":"AQAB"}`,
	} {
		if _, ok := parseJWK(json.RawMessage(bad)); ok {
			t.Errorf("lee %s", bad)
		}
	}
}

func TestOIDCConfig(t *testing.T) {
	f := newFakeOIDC(t)
	ok := oidcConfig(f)
	for _, tc := range []struct {
		name string
		edit func(*Config)
	}{
		{"sin límites", func(c *Config) { c.OIDC.AllowedDomains = nil }},
		{"límites y cualquiera", func(c *Config) { c.OIDC.AllowAny = true }},
		{"sin client id", func(c *Config) { c.OIDC.ClientID = "" }},
		{"sin emisor", func(c *Config) { c.OIDC.Issuer = "" }},
		{"emisor http fuera de loopback", func(c *Config) { c.OIDC.Issuer = "http://idp.example.com" }},
		{"emisor con query", func(c *Config) { c.OIDC.Issuer = f.URL + "?x=1" }},
		{"dominio con @ en medio", func(c *Config) { c.OIDC.AllowedDomains = []string{"ana@example.com"} }},
		{"falta SESSION_SECRET", func(c *Config) { c.SessionSecret = "" }},
		{"falta PUBLIC_URL", func(c *Config) { c.PublicURL = "" }},
	} {
		c := ok
		tc.edit(&c)
		if _, err := New(c, quiet); err == nil {
			t.Errorf("%s: debería fallar", tc.name)
		}
	}
	anyone := ok
	anyone.OIDC.AllowedDomains = nil
	anyone.OIDC.AllowAny = true
	if _, err := New(anyone, quiet); err != nil {
		t.Errorf("con OIDC_ALLOW_ANY=1: %v", err)
	}
	a, err := New(ok, quiet)
	if err != nil {
		t.Fatal(err)
	}
	if a.oidc.cfg.AllowedDomains[0] != "example.com" || a.oidc.cfg.GroupsClaim != "groups" {
		t.Errorf("cfg = %+v", a.oidc.cfg)
	}

	env := map[string]string{
		"OIDC_ISSUER": "https://idp.example.com", "OIDC_CLIENT_ID": "id", "OIDC_CLIENT_SECRET": "s",
		"OIDC_SCOPES": "email profile groups", "OIDC_NAME": "Acme SSO", "OIDC_ALLOWED_DOMAINS": "@Example.com, example.org",
		"OIDC_ALLOWED_GROUPS": "a,b", "OIDC_GROUPS_CLAIM": "roles",
	}
	c := LoadConfig(func(k string) string { return env[k] }, ":8080", publicURL)
	c.SessionSecret = testSecret
	a, err = New(c, quiet)
	if err != nil {
		t.Fatal(err)
	}
	got := a.oidc.cfg
	if got.Issuer != "https://idp.example.com" || got.ClientID != "id" || got.ClientSecret != "s" || got.Name != "Acme SSO" ||
		strings.Join(got.Scopes, " ") != "openid email profile groups" || strings.Join(got.AllowedDomains, ",") != "example.com,example.org" ||
		strings.Join(got.AllowedGroups, ",") != "a,b" || got.GroupsClaim != "roles" || got.AllowAny {
		t.Errorf("LoadConfig = %+v", got)
	}
	env["OIDC_ALLOW_ANY"] = "1"
	if !LoadConfig(func(k string) string { return env[k] }, "", "").OIDC.AllowAny {
		t.Error("OIDC_ALLOW_ANY=1 no se lee")
	}

	// Un emisor que no responde no impide arrancar: falla al entrar.
	down := ok
	down.OIDC.Issuer = "http://127.0.0.1:1"
	h := newHarness(t, down)
	if res := h.do(t, http.MethodGet, "/auth/oidc/login", nil, nil); res.Header.Get("Location") != "/login?error=oidc" {
		t.Errorf("emisor caído = %s", res.Header.Get("Location"))
	}
}

// Un descubrimiento con otro issuer o con endpoints http fuera de loopback no vale.
func TestOIDCDiscoveryChecks(t *testing.T) {
	var meta map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(meta)
	}))
	t.Cleanup(srv.Close)
	cfg := Config{SessionSecret: testSecret, PublicURL: publicURL, ListenAddr: ":8080",
		OIDC: OIDCConfig{Issuer: srv.URL, ClientID: "c", AllowAny: true}}
	for name, m := range map[string]map[string]any{
		"otro issuer": {"issuer": "https://otro.example", "authorization_endpoint": srv.URL + "/a", "token_endpoint": srv.URL + "/t", "jwks_uri": srv.URL + "/j"},
		"token http":  {"issuer": srv.URL, "authorization_endpoint": srv.URL + "/a", "token_endpoint": "http://idp.example.com/t", "jwks_uri": srv.URL + "/j"},
		"sin S256":    {"issuer": srv.URL, "authorization_endpoint": srv.URL + "/a", "token_endpoint": srv.URL + "/t", "jwks_uri": srv.URL + "/j", "code_challenge_methods_supported": []string{"plain"}},
	} {
		meta = m
		h := newHarness(t, cfg)
		if res := h.do(t, http.MethodGet, "/auth/oidc/login", nil, nil); res.Header.Get("Location") != "/login?error=oidc" {
			t.Errorf("%s = %s", name, res.Header.Get("Location"))
		}
	}
}

func TestBothProviders(t *testing.T) {
	gh := newFakeGitHub(t)
	f := newFakeOIDC(t)
	cfg := githubConfig(gh)
	cfg.OIDC = oidcConfig(f).OIDC
	h := newHarness(t, cfg)

	res := h.do(t, http.MethodGet, "/auth/session", nil, nil)
	var body sessionJSON
	_ = json.NewDecoder(res.Body).Decode(&body)
	if strings.Join(body.Methods, ",") != "github,oidc" || body.Org != "example-org" || body.OIDCName != "Acme SSO" {
		t.Errorf("/auth/session = %+v", body)
	}
	if res := h.login(t, "ana", "/"); cookieNamed(res, sessionCookie) == nil {
		t.Errorf("GitHub con OIDC al lado = %s", res.Header.Get("Location"))
	}
	if res := h.oidcLogin(t, f, fakeGrant{claims: user("u-1", "ana@example.com")}); cookieNamed(res, sessionCookie) == nil {
		t.Errorf("OIDC con GitHub al lado = %s", res.Header.Get("Location"))
	}

	// Sin poder guardar a la persona, con OIDC configurado no se entra: el
	// login de GitHub podría ser el de otra persona.
	h.auth.SavePeople(&savedPeople{fail: true})
	if res := h.login(t, "ana", "/"); res.Header.Get("Location") != "/login?error=save" || cookieNamed(res, sessionCookie) != nil {
		t.Errorf("GitHub sin guardar = %s", res.Header.Get("Location"))
	}
	if res := h.oidcLogin(t, f, fakeGrant{claims: user("u-1", "ana@example.com")}); res.Header.Get("Location") != "/login?error=save" {
		t.Errorf("OIDC sin guardar = %s", res.Header.Get("Location"))
	}

	// Solo GitHub: OIDC no está.
	only := newHarness(t, githubConfig(gh))
	if res := only.do(t, http.MethodGet, "/auth/oidc/login", nil, nil); res.Header.Get("Location") != "/login?error=oidc_config" {
		t.Errorf("OIDC sin configurar = %s", res.Header.Get("Location"))
	}
}
