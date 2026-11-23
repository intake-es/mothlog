package webauth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/intake-es/intake-errors/internal/store"
)

const (
	oidcStateCookie = "ie_oidc_state"
	// discoveryTTL y jwksTTL: cada cuánto se vuelven a pedir aunque no falte nada.
	discoveryTTL = time.Hour
	jwksTTL      = time.Hour
	// jwksMinRefresh evita pedir las claves en cada token con un kid desconocido.
	jwksMinRefresh = time.Minute
	clockLeeway    = time.Minute
)

// OIDCConfig es un proveedor OIDC genérico. Hace falta Issuer y ClientID, y
// un límite (AllowedDomains o AllowedGroups) o AllowAny. Con dominios y
// grupos a la vez, hay que cumplir los dos.
type OIDCConfig struct {
	Issuer       string
	ClientID     string
	ClientSecret string
	Scopes       []string
	// Name es el del botón: «Entrar con <Name>».
	Name           string
	AllowedDomains []string
	AllowedGroups  []string
	GroupsClaim    string
	AllowAny       bool
}

func (c OIDCConfig) configured() bool {
	return c.Issuer != "" || c.ClientID != "" || c.ClientSecret != ""
}

func loadOIDCConfig(getenv func(string) string) OIDCConfig {
	allowAny := strings.TrimSpace(getenv("OIDC_ALLOW_ANY"))
	return OIDCConfig{
		Issuer:         strings.TrimSpace(getenv("OIDC_ISSUER")),
		ClientID:       strings.TrimSpace(getenv("OIDC_CLIENT_ID")),
		ClientSecret:   strings.TrimSpace(getenv("OIDC_CLIENT_SECRET")),
		Scopes:         splitList(getenv("OIDC_SCOPES")),
		Name:           strings.TrimSpace(getenv("OIDC_NAME")),
		AllowedDomains: splitList(getenv("OIDC_ALLOWED_DOMAINS")),
		AllowedGroups:  splitList(getenv("OIDC_ALLOWED_GROUPS")),
		GroupsClaim:    strings.TrimSpace(getenv("OIDC_GROUPS_CLAIM")),
		AllowAny:       allowAny == "1" || strings.EqualFold(allowAny, "true"),
	}
}

// splitList separa por comas o espacios.
func splitList(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\n' })
}

type oidcProvider struct {
	cfg    OIDCConfig
	client *http.Client
	now    func() time.Time

	mu       sync.Mutex
	meta     *oidcMeta
	metaAt   time.Time
	keys     []jwk
	keysAt   time.Time
	keysTry  time.Time
	keysFrom string
}

type oidcMeta struct {
	Issuer                string   `json:"issuer"`
	AuthorizationEndpoint string   `json:"authorization_endpoint"`
	TokenEndpoint         string   `json:"token_endpoint"`
	JWKSURI               string   `json:"jwks_uri"`
	UserinfoEndpoint      string   `json:"userinfo_endpoint"`
	TokenAuthMethods      []string `json:"token_endpoint_auth_methods_supported"`
	CodeChallengeMethods  []string `json:"code_challenge_methods_supported"`
}

func newOIDCProvider(cfg OIDCConfig, client *http.Client, now func() time.Time) (*oidcProvider, error) {
	if cfg.Issuer == "" || cfg.ClientID == "" {
		return nil, errors.New("el login con OIDC necesita OIDC_ISSUER y OIDC_CLIENT_ID")
	}
	if err := checkEndpoint(cfg.Issuer, cfg.Issuer); err != nil {
		return nil, fmt.Errorf("OIDC_ISSUER: %w", err)
	}
	if u, _ := url.Parse(cfg.Issuer); u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("OIDC_ISSUER no puede llevar ? ni #")
	}
	domains := make([]string, 0, len(cfg.AllowedDomains))
	for _, d := range cfg.AllowedDomains {
		d = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(d), "@"))
		if d == "" || strings.ContainsAny(d, "@/") {
			return nil, fmt.Errorf("OIDC_ALLOWED_DOMAINS: %q no es un dominio", d)
		}
		domains = append(domains, d)
	}
	cfg.AllowedDomains = domains
	limited := len(cfg.AllowedDomains) > 0 || len(cfg.AllowedGroups) > 0
	switch {
	case !limited && !cfg.AllowAny:
		return nil, errors.New("el login con OIDC necesita OIDC_ALLOWED_DOMAINS u OIDC_ALLOWED_GROUPS; para dejar entrar a cualquiera con cuenta en el emisor, OIDC_ALLOW_ANY=1")
	case limited && cfg.AllowAny:
		return nil, errors.New("OIDC_ALLOW_ANY=1 deja entrar a cualquiera: no va con OIDC_ALLOWED_DOMAINS ni OIDC_ALLOWED_GROUPS")
	}
	if len(cfg.Scopes) == 0 {
		cfg.Scopes = []string{"openid", "email", "profile"}
	}
	if !slices.Contains(cfg.Scopes, "openid") {
		cfg.Scopes = append([]string{"openid"}, cfg.Scopes...)
	}
	if cfg.GroupsClaim == "" {
		cfg.GroupsClaim = "groups"
	}
	if cfg.Name == "" {
		cfg.Name = "SSO"
	}
	return &oidcProvider{cfg: cfg, client: client, now: now}, nil
}

// checkEndpoint exige https, salvo en loopback y solo si el emisor también
// lo es, para probar con un proveedor local.
func checkEndpoint(raw, issuer string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("%q no es una URL absoluta", raw)
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		iu, _ := url.Parse(issuer)
		if loopbackHost(u.Hostname()) && iu != nil && iu.Scheme == "http" && loopbackHost(iu.Hostname()) {
			return nil
		}
	}
	return fmt.Errorf("%q tiene que ser https", raw)
}

func (p *oidcProvider) getJSON(ctx context.Context, rawURL, bearer string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	res, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %d", rawURL, res.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(v); err != nil {
		return fmt.Errorf("GET %s: %w", rawURL, err)
	}
	return nil
}

// discover lee /.well-known/openid-configuration y lo guarda una hora.
func (p *oidcProvider) discover(ctx context.Context) (*oidcMeta, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.meta != nil && p.now().Sub(p.metaAt) < discoveryTTL {
		return p.meta, nil
	}
	var m oidcMeta
	if err := p.getJSON(ctx, strings.TrimSuffix(p.cfg.Issuer, "/")+"/.well-known/openid-configuration", "", &m); err != nil {
		return nil, fmt.Errorf("descubrimiento: %w", err)
	}
	if m.Issuer != p.cfg.Issuer {
		return nil, fmt.Errorf("descubrimiento: issuer %q no es OIDC_ISSUER %q", m.Issuer, p.cfg.Issuer)
	}
	for _, e := range []string{m.AuthorizationEndpoint, m.TokenEndpoint, m.JWKSURI} {
		if err := checkEndpoint(e, p.cfg.Issuer); err != nil {
			return nil, fmt.Errorf("descubrimiento: %w", err)
		}
	}
	if m.UserinfoEndpoint != "" && checkEndpoint(m.UserinfoEndpoint, p.cfg.Issuer) != nil {
		m.UserinfoEndpoint = ""
	}
	if len(m.CodeChallengeMethods) > 0 && !slices.Contains(m.CodeChallengeMethods, "S256") {
		return nil, errors.New("descubrimiento: el emisor no admite PKCE con S256")
	}
	p.meta, p.metaAt = &m, p.now()
	return p.meta, nil
}

// signingKeys devuelve las claves del emisor; con refresh las vuelve a pedir
// (un kid nuevo tras una rotación), como mucho una vez por minuto.
func (p *oidcProvider) signingKeys(ctx context.Context, jwksURI string, refresh bool) ([]jwk, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	fresh := p.keys != nil && p.keysFrom == jwksURI && now.Sub(p.keysAt) < jwksTTL
	if fresh && (!refresh || now.Sub(p.keysTry) < jwksMinRefresh) {
		return p.keys, nil
	}
	p.keysTry = now
	var set struct {
		Keys []json.RawMessage `json:"keys"`
	}
	if err := p.getJSON(ctx, jwksURI, "", &set); err != nil {
		if fresh {
			return p.keys, nil
		}
		return nil, fmt.Errorf("JWKS: %w", err)
	}
	keys := []jwk{}
	for _, raw := range set.Keys {
		if k, ok := parseJWK(raw); ok {
			keys = append(keys, k)
		}
	}
	p.keys, p.keysAt, p.keysFrom = keys, now, jwksURI
	return keys, nil
}

func (p *oidcProvider) verifyIDToken(ctx context.Context, meta *oidcMeta, token, nonce string) (map[string]any, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, errors.New("id_token: no es un JWT")
	}
	var header struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := decodeSegment(parts[0], &header); err != nil {
		return nil, fmt.Errorf("id_token: cabecera: %w", err)
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, errors.New("id_token: firma ilegible")
	}
	signed := []byte(parts[0] + "." + parts[1])
	verified := false
	for _, refresh := range []bool{false, true} {
		keys, err := p.signingKeys(ctx, meta.JWKSURI, refresh)
		if err != nil {
			return nil, err
		}
		known := false
		for _, k := range keys {
			if header.Kid != "" && k.kid != header.Kid {
				continue
			}
			known = true
			if k.verify(header.Alg, signed, sig) == nil {
				verified = true
				break
			}
		}
		if verified || (known && header.Kid != "") {
			break
		}
	}
	if !verified {
		return nil, fmt.Errorf("id_token: firma no válida (alg %q, kid %q)", header.Alg, header.Kid)
	}

	var claims map[string]any
	if err := decodeSegment(parts[1], &claims); err != nil {
		return nil, fmt.Errorf("id_token: claims: %w", err)
	}
	if iss, _ := claims["iss"].(string); iss != p.cfg.Issuer {
		return nil, fmt.Errorf("id_token: iss %q", iss)
	}
	aud := stringList(claims["aud"])
	if !slices.Contains(aud, p.cfg.ClientID) {
		return nil, fmt.Errorf("id_token: aud %v", aud)
	}
	if azp, ok := claims["azp"].(string); (ok || len(aud) > 1) && azp != p.cfg.ClientID {
		return nil, fmt.Errorf("id_token: azp %q", azp)
	}
	now := p.now()
	exp, ok := numericDate(claims["exp"])
	if !ok || !now.Before(exp.Add(clockLeeway)) {
		return nil, fmt.Errorf("id_token: caducado (exp %v)", claims["exp"])
	}
	if iat, ok := numericDate(claims["iat"]); ok && iat.After(now.Add(clockLeeway)) {
		return nil, errors.New("id_token: iat en el futuro")
	}
	if nbf, ok := numericDate(claims["nbf"]); ok && nbf.After(now.Add(clockLeeway)) {
		return nil, errors.New("id_token: todavía no vale (nbf)")
	}
	got, _ := claims["nonce"].(string)
	if nonce == "" || subtle.ConstantTimeCompare([]byte(got), []byte(nonce)) != 1 {
		return nil, errors.New("id_token: el nonce no cuadra")
	}
	if sub, _ := claims["sub"].(string); sub == "" {
		return nil, errors.New("id_token: sin sub")
	}
	return claims, nil
}

func decodeSegment(seg string, v any) error {
	b, err := base64.RawURLEncoding.DecodeString(seg)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func numericDate(v any) (time.Time, bool) {
	f, ok := v.(float64)
	if !ok {
		return time.Time{}, false
	}
	return time.Unix(int64(f), 0), true
}

// stringList lee un claim que puede ser una cadena o una lista de cadenas.
func stringList(v any) []string {
	switch x := v.(type) {
	case string:
		if x == "" {
			return nil
		}
		return []string{x}
	case []any:
		out := make([]string, 0, len(x))
		for _, e := range x {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func boolClaim(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		return strings.EqualFold(x, "true")
	}
	return false
}

func (p *oidcProvider) exchange(ctx context.Context, meta *oidcMeta, code, verifier, redirect string) (idToken, accessToken string, err error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirect},
		"code_verifier": {verifier},
		"client_id":     {p.cfg.ClientID},
	}
	basic := p.cfg.ClientSecret != "" &&
		(len(meta.TokenAuthMethods) == 0 || slices.Contains(meta.TokenAuthMethods, "client_secret_basic"))
	if p.cfg.ClientSecret != "" && !basic {
		form.Set("client_secret", p.cfg.ClientSecret)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, meta.TokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if basic {
		req.SetBasicAuth(url.QueryEscape(p.cfg.ClientID), url.QueryEscape(p.cfg.ClientSecret))
	}
	res, err := p.client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer func() { _ = res.Body.Close() }()
	var body struct {
		IDToken     string `json:"id_token"`
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&body); err != nil {
		return "", "", fmt.Errorf("token: respuesta %d: %w", res.StatusCode, err)
	}
	if res.StatusCode != http.StatusOK || body.IDToken == "" {
		return "", "", fmt.Errorf("token: respuesta %d sin id_token: %q", res.StatusCode, body.Error)
	}
	return body.IDToken, body.AccessToken, nil
}

// fillFromUserinfo completa con /userinfo los claims que hacen falta para
// decidir y no trae el id_token. Los del id_token mandan.
func (p *oidcProvider) fillFromUserinfo(ctx context.Context, meta *oidcMeta, accessToken string, claims map[string]any) error {
	need := []string{}
	if len(p.cfg.AllowedDomains) > 0 {
		need = append(need, "email", "email_verified")
	}
	if len(p.cfg.AllowedGroups) > 0 {
		need = append(need, p.cfg.GroupsClaim)
	}
	missing := false
	for _, k := range need {
		if _, ok := claims[k]; !ok {
			missing = true
		}
	}
	if !missing || meta.UserinfoEndpoint == "" || accessToken == "" {
		return nil
	}
	var info map[string]any
	if err := p.getJSON(ctx, meta.UserinfoEndpoint, accessToken, &info); err != nil {
		return fmt.Errorf("userinfo: %w", err)
	}
	if info["sub"] != claims["sub"] {
		return errors.New("userinfo: el sub no es el del id_token")
	}
	for k, v := range info {
		if _, ok := claims[k]; !ok {
			claims[k] = v
		}
	}
	return nil
}

// allowed dice si los claims pasan los límites; si no, el motivo para la
// pantalla de login.
func (p *oidcProvider) allowed(claims map[string]any) (bool, string) {
	if len(p.cfg.AllowedDomains) > 0 {
		email, _ := claims["email"].(string)
		at := strings.LastIndexByte(email, '@')
		if at < 0 || !boolClaim(claims["email_verified"]) ||
			!slices.Contains(p.cfg.AllowedDomains, strings.ToLower(email[at+1:])) {
			return false, "domain"
		}
	}
	if len(p.cfg.AllowedGroups) > 0 {
		groups := stringList(claims[p.cfg.GroupsClaim])
		if !slices.ContainsFunc(groups, func(g string) bool { return slices.Contains(p.cfg.AllowedGroups, g) }) {
			return false, "group"
		}
	}
	return true, ""
}

type oidcState struct {
	State    string `json:"s"`
	Nonce    string `json:"o"`
	Verifier string `json:"v"`
	Next     string `json:"n"`
	Expires  int64  `json:"e"`
}

func (a *Auth) oidcCallbackURL() string { return a.cfg.PublicURL + "/auth/oidc/callback" }

func (a *Auth) handleOIDCLogin(w http.ResponseWriter, r *http.Request) {
	if a.oidc == nil {
		toLogin(w, r, "oidc_config")
		return
	}
	meta, err := a.oidc.discover(r.Context())
	if err != nil {
		a.log.Warn("login oidc: no se pudo leer el emisor", "err", err)
		toLogin(w, r, "oidc")
		return
	}
	st := oidcState{
		State:    randomHex(32),
		Nonce:    randomHex(32),
		Verifier: base64.RawURLEncoding.EncodeToString([]byte(randomHex(32))),
		Next:     safeNext(r.URL.Query().Get("next")),
		Expires:  a.cfg.Now().Add(stateTTL).Unix(),
	}
	v, err := a.sign("oidc-state", st)
	if err != nil {
		http.Error(w, "error interno", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, a.cookie(oidcStateCookie, v, "/auth/oidc/", stateTTL))
	challenge := sha256.Sum256([]byte(st.Verifier))
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {a.oidc.cfg.ClientID},
		"redirect_uri":          {a.oidcCallbackURL()},
		"scope":                 {strings.Join(a.oidc.cfg.Scopes, " ")},
		"state":                 {st.State},
		"nonce":                 {st.Nonce},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(challenge[:])},
		"code_challenge_method": {"S256"},
	}
	sep := "?"
	if strings.Contains(meta.AuthorizationEndpoint, "?") {
		sep = "&"
	}
	http.Redirect(w, r, meta.AuthorizationEndpoint+sep+q.Encode(), http.StatusFound)
}

// oidcLoginPrefix va delante del login de OIDC que no es un correo verificado.
const oidcLoginPrefix = "oidc:"

func (a *Auth) handleOIDCCallback(w http.ResponseWriter, r *http.Request) {
	if a.oidc == nil {
		toLogin(w, r, "oidc_config")
		return
	}
	http.SetCookie(w, a.cookie(oidcStateCookie, "", "/auth/oidc/", -1))
	q := r.URL.Query()
	var st oidcState
	c, err := r.Cookie(oidcStateCookie)
	if err != nil || !a.verify("oidc-state", c.Value, &st) || a.cfg.Now().Unix() >= st.Expires ||
		subtle.ConstantTimeCompare([]byte(st.State), []byte(q.Get("state"))) != 1 {
		a.log.Warn("login rechazado: state no válido", "provider", store.LoginOIDC)
		toLogin(w, r, "state")
		return
	}
	if iss := q.Get("iss"); iss != "" && iss != a.oidc.cfg.Issuer {
		a.log.Warn("login rechazado: iss de la respuesta no es el emisor", "provider", store.LoginOIDC, "iss", iss)
		toLogin(w, r, "oidc")
		return
	}
	if q.Get("error") != "" || q.Get("code") == "" {
		a.log.Info("login cancelado en el proveedor", "provider", store.LoginOIDC, "error", q.Get("error"))
		toLogin(w, r, "oidc_denied")
		return
	}

	ctx := r.Context()
	meta, err := a.oidc.discover(ctx)
	if err != nil {
		a.log.Warn("login oidc: no se pudo leer el emisor", "err", err)
		toLogin(w, r, "oidc")
		return
	}
	idToken, accessToken, err := a.oidc.exchange(ctx, meta, q.Get("code"), st.Verifier, a.oidcCallbackURL())
	if err != nil {
		a.log.Warn("login oidc: no se pudo canjear el código", "err", err)
		toLogin(w, r, "oidc")
		return
	}
	claims, err := a.oidc.verifyIDToken(ctx, meta, idToken, st.Nonce)
	if err != nil {
		a.log.Warn("login rechazado: id_token no válido", "provider", store.LoginOIDC, "err", err)
		toLogin(w, r, "oidc")
		return
	}
	sub, _ := claims["sub"].(string)
	if err := a.oidc.fillFromUserinfo(ctx, meta, accessToken, claims); err != nil {
		a.log.Warn("login oidc: userinfo", "sub", sub, "err", err)
		toLogin(w, r, "oidc")
		return
	}
	if ok, reason := a.oidc.allowed(claims); !ok {
		// Del rechazado, solo el sub y el dominio del correo: no se guarda nada más.
		email, _ := claims["email"].(string)
		_, domain, _ := strings.Cut(email, "@")
		a.log.Warn("login rechazado: fuera de los límites de OIDC", "provider", store.LoginOIDC, "reason", reason,
			"sub", sub, "email_domain", domain, "email_verified", boolClaim(claims["email_verified"]))
		toLogin(w, r, reason)
		return
	}

	email, _ := claims["email"].(string)
	username, _ := claims["preferred_username"].(string)
	// Un correo sin verificar podría ser el de otra persona: no sirve de
	// login. preferred_username tampoco es de fiar, así que va con el prefijo
	// oidc:, que no puede ser un correo ni un login de GitHub.
	shown := ""
	if boolClaim(claims["email_verified"]) {
		shown = email
	}
	if shown == "" && username != "" {
		shown = oidcLoginPrefix + username
	}
	if shown == "" {
		shown = oidcLoginPrefix + sub
	}
	name, _ := claims["name"].(string)
	if name == "" {
		name = shown
	}
	var avatar *string
	if pic, ok := claims["picture"].(string); ok && strings.HasPrefix(pic, "https://") {
		avatar = &pic
	}
	login, ok := a.savePerson(ctx, store.Person{Login: shown, Name: name, AvatarURL: avatar, Provider: store.LoginOIDC, Subject: sub})
	if !ok {
		toLogin(w, r, "save")
		return
	}
	// Quien entró antes de que existiera el prefijo pudo quedarse con un
	// correo o un login de GitHub ajeno: sin correo verificado, pasa a oidc:.
	if strings.HasPrefix(shown, oidcLoginPrefix) && !strings.HasPrefix(login, oidcLoginPrefix) {
		renamed, err := a.people.RenamePerson(ctx, login, oidcLoginPrefix+login)
		if err != nil {
			a.log.Error("login oidc: poner el prefijo al login", "login", login, "sub", sub, "err", err)
			toLogin(w, r, "save")
			return
		}
		a.log.Warn("login oidc: el login sin correo verificado pasa a llevar el prefijo", "from", login, "to", renamed, "sub", sub)
		login = renamed
	}
	if err := a.setSession(w, login, name); err != nil {
		http.Error(w, "error interno", http.StatusInternalServerError)
		return
	}
	a.log.Info("login", "provider", store.LoginOIDC, "login", login, "sub", sub)
	http.Redirect(w, r, st.Next, http.StatusFound)
}
