package oauth

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/intake-es/intake-errors/internal/api"
	"github.com/intake-es/intake-errors/internal/store"
)

// pkceValue son los 43 a 128 caracteres de RFC 7636.
var pkceValue = regexp.MustCompile(`^[A-Za-z0-9._~-]{43,128}$`)

const maxState = 1000

// errGrant: el código no vale para esta petición de token.
var errGrant = errors.New("invalid_grant")

// consentPath es la pantalla del panel donde la persona da su consentimiento.
const consentPath = "/authorize"

// requestedScopes son los permisos conocidos que pide scope; sin ninguno, todos.
func requestedScopes(scope string) []string {
	var out []string
	for _, sc := range scopes {
		if slices.Contains(strings.Fields(scope), sc) {
			out = append(out, sc)
		}
	}
	if len(out) == 0 {
		return slices.Clone(scopes)
	}
	return out
}

// sameResource: el recurso pedido es el MCP, con o sin barra final.
func (s *Server) sameResource(r string) bool {
	return strings.TrimRight(r, "/") == s.resource
}

// toPanel lleva al panel a explicar un error que no se puede mandar al
// cliente porque no se sabe si su URI de vuelta es suya.
func (s *Server) toPanel(w http.ResponseWriter, r *http.Request, reason string) {
	http.Redirect(w, r, s.issuer+consentPath+"?error="+url.QueryEscape(reason), http.StatusFound)
}

// withParams añade params a la URI de vuelta, respetando su query.
func withParams(redirect string, params url.Values) string {
	u, err := url.Parse(redirect)
	if err != nil {
		return redirect
	}
	q := u.Query()
	for k, v := range params {
		q[k] = v
	}
	u.RawQuery = q.Encode()
	return u.String()
}

func (s *Server) errorRedirect(w http.ResponseWriter, r *http.Request, redirect, state, kind, desc string) {
	p := url.Values{"error": {kind}, "error_description": {desc}, "iss": {s.issuer}}
	if state != "" {
		p.Set("state", state)
	}
	http.Redirect(w, r, withParams(redirect, p), http.StatusFound)
}

// authorize valida la petición y manda a la persona a la pantalla de
// consentimiento del panel, que le pide entrar si no tiene sesión.
func (s *Server) authorize(w http.ResponseWriter, r *http.Request) {
	if !s.authorizations.allow(clientIP(r), s.cfg.Now()) {
		w.Header().Set("Retry-After", strconv.Itoa(int(limitWindow.Seconds())))
		http.Error(w, "demasiadas peticiones de autorización desde esta dirección; vuelve a probar en unos minutos", http.StatusTooManyRequests)
		return
	}
	q := r.URL.Query()
	ctx := r.Context()
	c, err := s.lookupClient(ctx, q.Get("client_id"))
	if errors.Is(err, errClient) {
		s.log.Warn("oauth: autorizar con un cliente desconocido", "client_id", q.Get("client_id"), "err", err)
		s.toPanel(w, r, "client")
		return
	}
	if err != nil {
		s.log.Error("oauth: leer cliente", "err", err)
		s.toPanel(w, r, "server")
		return
	}
	redirect := q.Get("redirect_uri")
	// Sin redirect_uri no se acepta: el canje la exige igual que llegó aquí.
	if !redirectMatches(c.RedirectURIs, redirect) {
		s.log.Warn("oauth: redirect_uri que no es del cliente", "client_id", c.ID, "redirect_uri", redirect)
		s.toPanel(w, r, "redirect")
		return
	}
	state := q.Get("state")
	if len(state) > maxState {
		s.errorRedirect(w, r, redirect, "", "invalid_request", "state es demasiado largo")
		return
	}
	switch {
	case q.Get("response_type") != "code":
		s.errorRedirect(w, r, redirect, state, "unsupported_response_type", "solo response_type=code")
		return
	case q.Get("code_challenge") == "" || q.Get("code_challenge_method") != "S256":
		s.errorRedirect(w, r, redirect, state, "invalid_request", "hace falta PKCE con code_challenge_method=S256")
		return
	case !pkceValue.MatchString(q.Get("code_challenge")):
		s.errorRedirect(w, r, redirect, state, "invalid_request", "code_challenge no es válido")
		return
	case q.Get("resource") == "":
		s.errorRedirect(w, r, redirect, state, "invalid_target", "falta resource, que es "+s.resource)
		return
	case !s.sameResource(q.Get("resource")):
		s.errorRedirect(w, r, redirect, state, "invalid_target", "este servidor solo da acceso a "+s.resource)
		return
	}
	now := s.cfg.Now()
	req := store.OAuthRequest{
		ID: randomID(), ClientID: c.ID, ClientName: c.Name, RedirectURI: redirect,
		Scopes: requestedScopes(q.Get("scope")), State: state, CodeChallenge: q.Get("code_challenge"),
		Resource: s.resource, ExpiresAt: now.Add(requestTTL),
	}
	if err := s.st.CreateOAuthRequest(ctx, req, now); err != nil {
		s.log.Error("oauth: guardar petición", "err", err)
		s.errorRedirect(w, r, redirect, state, "server_error", "error interno")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, s.issuer+consentPath+"?request="+req.ID, http.StatusFound)
}

type requestJSON struct {
	ClientName  string   `json:"client_name"`
	ClientID    string   `json:"client_id"`
	ClientURL   bool     `json:"client_url"`
	RedirectURI string   `json:"redirect_uri"`
	Scopes      []string `json:"scopes"`
	ExpiresAt   string   `json:"expires_at"`
}

func (s *Server) pendingRequest(w http.ResponseWriter, r *http.Request) {
	req, err := s.st.PendingOAuthRequest(r.Context(), r.PathValue("id"), s.cfg.Now())
	if !s.ok(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, requestJSON{
		ClientName: req.ClientName, ClientID: req.ClientID, ClientURL: strings.HasPrefix(req.ClientID, "https://"),
		RedirectURI: req.RedirectURI, Scopes: req.Scopes, ExpiresAt: req.ExpiresAt.UTC().Format(time.RFC3339),
	})
}

func (s *Server) ok(w http.ResponseWriter, r *http.Request, err error) bool {
	switch {
	case err == nil:
		return true
	case errors.Is(err, store.ErrNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "esta petición ya no existe, ha caducado o ya se respondió"})
	default:
		s.log.ErrorContext(r.Context(), "oauth", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "error interno"})
	}
	return false
}

// approve: lo concedido es lo marcado, y nunca más de lo pedido.
func (s *Server) approve(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Scopes []string `json:"scopes"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "cuerpo no válido"})
		return
	}
	ctx := r.Context()
	now := s.cfg.Now()
	pending, err := s.st.PendingOAuthRequest(ctx, r.PathValue("id"), now)
	if !s.ok(w, r, err) {
		return
	}
	var granted []string
	for _, sc := range pending.Scopes {
		if slices.Contains(body.Scopes, sc) {
			granted = append(granted, sc)
		}
	}
	if len(granted) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "marca al menos un permiso de los que pide"})
		return
	}
	p, _ := api.PrincipalFrom(ctx)
	req, code, err := s.st.ApproveOAuthRequest(ctx, pending.ID, p.Name, granted, now.Add(codeTTL), now)
	if !s.ok(w, r, err) {
		return
	}
	s.log.Info("oauth: consentimiento", "login", p.Name, "client_id", req.ClientID, "client", req.ClientName, "scopes", strings.Join(granted, " "))
	params := url.Values{"code": {code}, "iss": {s.issuer}}
	if req.State != "" {
		params.Set("state", req.State)
	}
	writeJSON(w, http.StatusOK, map[string]string{"redirect_to": withParams(req.RedirectURI, params)})
}

func (s *Server) deny(w http.ResponseWriter, r *http.Request) {
	req, err := s.st.DenyOAuthRequest(r.Context(), r.PathValue("id"), s.cfg.Now())
	if !s.ok(w, r, err) {
		return
	}
	params := url.Values{"error": {"access_denied"}, "error_description": {"la persona no ha dado acceso"}, "iss": {s.issuer}}
	if req.State != "" {
		params.Set("state", req.State)
	}
	writeJSON(w, http.StatusOK, map[string]string{"redirect_to": withParams(req.RedirectURI, params)})
}

func (s *Server) grants(w http.ResponseWriter, r *http.Request) {
	p, _ := api.PrincipalFrom(r.Context())
	gs, err := s.st.OAuthGrants(r.Context(), p.Name, s.cfg.Now())
	if !s.ok(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": gs})
}

func (s *Server) revokeGrant(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no existe esa aplicación"})
		return
	}
	p, _ := api.PrincipalFrom(r.Context())
	err = s.st.RevokeOAuthGrant(r.Context(), p.Name, id, s.cfg.Now())
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no existe esa aplicación"})
		return
	}
	if !s.ok(w, r, err) {
		return
	}
	s.log.Info("oauth: concesión revocada en el panel", "login", p.Name, "grant", id)
	w.WriteHeader(http.StatusNoContent)
}

func s256(verifier string) string {
	h := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(h[:])
}

func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	cors(w)
	w.Header().Set("Pragma", "no-cache")
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_request", "el cuerpo tiene que ir como application/x-www-form-urlencoded")
		return
	}
	f := r.PostForm
	clientID := f.Get("client_id")
	if clientID == "" {
		oauthError(w, http.StatusUnauthorized, "invalid_client", "falta client_id")
		return
	}
	if res := f.Get("resource"); res != "" && !s.sameResource(res) {
		oauthError(w, http.StatusBadRequest, "invalid_target", "este servidor solo da acceso a "+s.resource)
		return
	}
	now := s.cfg.Now()
	var toks store.OAuthTokens
	var err error
	switch f.Get("grant_type") {
	case "authorization_code":
		if !pkceValue.MatchString(f.Get("code_verifier")) {
			oauthError(w, http.StatusBadRequest, "invalid_request", "falta code_verifier o no es válido")
			return
		}
		toks, err = s.st.ExchangeOAuthCode(r.Context(), f.Get("code"), func(req store.OAuthRequest) error {
			switch {
			case req.ClientID != clientID:
				return fmt.Errorf("%w: el código es de otro cliente", errGrant)
			case f.Get("redirect_uri") != req.RedirectURI:
				return fmt.Errorf("%w: redirect_uri no es la de la autorización", errGrant)
			case subtle.ConstantTimeCompare([]byte(s256(f.Get("code_verifier"))), []byte(req.CodeChallenge)) != 1:
				return fmt.Errorf("%w: code_verifier no corresponde a code_challenge", errGrant)
			case !s.sameResource(req.Resource):
				return fmt.Errorf("%w: el código es de otro recurso", errGrant)
			}
			return nil
		}, s.cfg.TTL, now)
	case "refresh_token":
		var narrow []string
		if sc := f.Get("scope"); sc != "" {
			narrow = strings.Fields(sc)
		}
		toks, err = s.st.RefreshOAuth(r.Context(), f.Get("refresh_token"), clientID, narrow, s.cfg.TTL, now)
	default:
		oauthError(w, http.StatusBadRequest, "unsupported_grant_type", "solo authorization_code y refresh_token")
		return
	}
	switch {
	case errors.Is(err, store.ErrOAuthReused):
		s.log.Warn("oauth: código o refresco reusado: concesión revocada", "client_id", clientID, "grant_type", f.Get("grant_type"))
		oauthError(w, http.StatusBadRequest, "invalid_grant", "ya se había usado: la concesión queda revocada")
		return
	case errors.Is(err, store.ErrInvalid):
		oauthError(w, http.StatusBadRequest, "invalid_scope", err.Error())
		return
	case errors.Is(err, store.ErrOAuthExpired), errors.Is(err, store.ErrNotFound):
		oauthError(w, http.StatusBadRequest, "invalid_grant", "no vale, ha caducado o se revocó")
		return
	case errors.Is(err, errGrant):
		oauthError(w, http.StatusBadRequest, "invalid_grant", strings.TrimPrefix(err.Error(), errGrant.Error()+": "))
		return
	case err != nil:
		s.log.Error("oauth: token", "err", err)
		oauthError(w, http.StatusInternalServerError, "server_error", "error interno")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token":  toks.Access,
		"token_type":    "Bearer",
		"expires_in":    int(toks.ExpiresIn.Seconds()),
		"refresh_token": toks.Refresh,
		"scope":         strings.Join(toks.Scopes, " "),
	})
}

// revoke es RFC 7009: responde 200 aunque el token no exista.
func (s *Server) revoke(w http.ResponseWriter, r *http.Request) {
	cors(w)
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil || r.PostForm.Get("token") == "" {
		oauthError(w, http.StatusBadRequest, "invalid_request", "falta token")
		return
	}
	clientID := r.PostForm.Get("client_id")
	if clientID == "" {
		oauthError(w, http.StatusUnauthorized, "invalid_client", "falta client_id")
		return
	}
	if err := s.st.RevokeOAuthToken(r.Context(), r.PostForm.Get("token"), clientID, s.cfg.Now()); err != nil {
		s.log.Error("oauth: revocar", "err", err)
		oauthError(w, http.StatusServiceUnavailable, "temporarily_unavailable", "no se ha podido revocar")
		return
	}
	w.WriteHeader(http.StatusOK)
}
