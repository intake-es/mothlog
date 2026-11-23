package webauth

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/intake-es/intake-errors/internal/store"
)

// Register añade /auth/session, /auth/github/login, /auth/github/callback,
// /auth/oidc/login, /auth/oidc/callback, /auth/dev/login y /auth/logout.
func (a *Auth) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /auth/session", a.handleSession)
	mux.HandleFunc("GET /auth/github/login", a.handleGitHubLogin)
	mux.HandleFunc("GET /auth/github/callback", a.handleGitHubCallback)
	mux.HandleFunc("GET /auth/oidc/login", a.handleOIDCLogin)
	mux.HandleFunc("GET /auth/oidc/callback", a.handleOIDCCallback)
	mux.HandleFunc("GET /auth/dev/login", a.handleDevLogin)
	mux.HandleFunc("POST /auth/logout", a.handleLogout)
}

type sessionJSON struct {
	User    *userJSON `json:"user"`
	Methods []string  `json:"methods"`
	Org     string    `json:"org,omitempty"`
	// OIDCName es el nombre del botón de OIDC.
	OIDCName string `json:"oidc_name,omitempty"`
}

type userJSON struct {
	Login string `json:"login"`
	Name  string `json:"name"`
}

func (a *Auth) handleSession(w http.ResponseWriter, r *http.Request) {
	out := sessionJSON{Methods: a.methods()}
	if a.github {
		out.Org = a.cfg.Org
	}
	if a.oidc != nil {
		out.OIDCName = a.oidc.cfg.Name
	}
	if s, ok := a.session(r); ok {
		out.User = &userJSON{Login: s.Login, Name: s.Name}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(out)
}

// toLogin vuelve a la pantalla de login con el motivo en ?error=.
func toLogin(w http.ResponseWriter, r *http.Request, reason string) {
	http.Redirect(w, r, "/login?error="+url.QueryEscape(reason), http.StatusFound)
}

func (a *Auth) handleGitHubLogin(w http.ResponseWriter, r *http.Request) {
	if !a.github {
		toLogin(w, r, "config")
		return
	}
	st := oauthState{
		State:   randomHex(32),
		Next:    safeNext(r.URL.Query().Get("next")),
		Expires: a.cfg.Now().Add(stateTTL).Unix(),
	}
	v, err := a.sign("state", st)
	if err != nil {
		http.Error(w, "error interno", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, a.cookie(stateCookie, v, "/auth/github/", stateTTL))
	q := url.Values{
		"client_id":    {a.cfg.ClientID},
		"redirect_uri": {a.callbackURL()},
		"scope":        {"read:org"},
		"state":        {st.State},
		"allow_signup": {"false"},
	}
	http.Redirect(w, r, a.cfg.GitHubURL+"/login/oauth/authorize?"+q.Encode(), http.StatusFound)
}

func (a *Auth) callbackURL() string { return a.cfg.PublicURL + "/auth/github/callback" }

func (a *Auth) handleGitHubCallback(w http.ResponseWriter, r *http.Request) {
	if !a.github {
		toLogin(w, r, "config")
		return
	}
	http.SetCookie(w, a.cookie(stateCookie, "", "/auth/github/", -1))
	q := r.URL.Query()
	var st oauthState
	c, err := r.Cookie(stateCookie)
	if err != nil || !a.verify("state", c.Value, &st) || a.cfg.Now().Unix() >= st.Expires ||
		subtle.ConstantTimeCompare([]byte(st.State), []byte(q.Get("state"))) != 1 {
		a.log.Warn("login: state no válido")
		toLogin(w, r, "state")
		return
	}
	if q.Get("error") != "" || q.Get("code") == "" {
		toLogin(w, r, "denied")
		return
	}

	ctx := r.Context()
	token, err := a.exchange(ctx, q.Get("code"))
	if err != nil {
		a.log.Warn("login: no se pudo canjear el código de GitHub", "err", err)
		toLogin(w, r, "github")
		return
	}
	var user struct {
		ID        int64  `json:"id"`
		Login     string `json:"login"`
		Name      string `json:"name"`
		AvatarURL string `json:"avatar_url"`
	}
	if code, err := a.githubGet(ctx, token, "/user", &user); err != nil || code != http.StatusOK || user.Login == "" || user.ID <= 0 {
		a.log.Warn("login: GET /user", "status", code, "err", err)
		toLogin(w, r, "github")
		return
	}
	var membership struct {
		State string `json:"state"`
	}
	code, err := a.githubGet(ctx, token, "/user/memberships/orgs/"+url.PathEscape(a.cfg.Org), &membership)
	switch {
	case err != nil || code >= 500:
		a.log.Warn("login: membresía", "status", code, "err", err)
		toLogin(w, r, "github")
		return
	case code != http.StatusOK || membership.State != "active":
		a.log.Warn("login rechazado: no es miembro activo de la org", "login", user.Login, "org", a.cfg.Org, "status", code, "state", membership.State)
		toLogin(w, r, "org")
		return
	}

	name := user.Name
	if name == "" {
		name = user.Login
	}
	// El login de GitHub se puede cambiar y otro lo puede coger: la persona
	// es el id. Si entró antes de guardarse por id, su fila se reconoce por el login.
	login, ok := a.savePerson(ctx, store.Person{Login: user.Login, Name: name, AvatarURL: &user.AvatarURL,
		Provider: store.LoginGitHub, Subject: strconv.FormatInt(user.ID, 10), LegacySubject: store.LegacyGitHubSubject(user.Login)})
	if !ok {
		toLogin(w, r, "save")
		return
	}
	if err := a.setSession(w, login, name); err != nil {
		http.Error(w, "error interno", http.StatusInternalServerError)
		return
	}
	a.log.Info("login", "provider", store.LoginGitHub, "login", login, "github_id", user.ID)
	http.Redirect(w, r, st.Next, http.StatusFound)
}

func (a *Auth) exchange(ctx context.Context, code string) (string, error) {
	form := url.Values{
		"client_id":     {a.cfg.ClientID},
		"client_secret": {a.cfg.ClientSecret},
		"code":          {code},
		"redirect_uri":  {a.callbackURL()},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.cfg.GitHubURL+"/login/oauth/access_token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	res, err := a.cfg.HTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = res.Body.Close() }()
	var body struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<16)).Decode(&body); err != nil {
		return "", fmt.Errorf("respuesta %d: %w", res.StatusCode, err)
	}
	if body.AccessToken == "" {
		return "", fmt.Errorf("respuesta %d sin access_token: %q", res.StatusCode, body.Error)
	}
	return body.AccessToken, nil
}

func (a *Auth) githubGet(ctx context.Context, token, path string, v any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.cfg.GitHubAPIURL+path, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	res, err := a.cfg.HTTPClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return res.StatusCode, nil
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(v); err != nil {
		return res.StatusCode, err
	}
	return res.StatusCode, nil
}

func (a *Auth) handleDevLogin(w http.ResponseWriter, r *http.Request) {
	if !a.cfg.DevLogin {
		http.NotFound(w, r)
		return
	}
	if !a.hostAllowed(r) {
		a.log.Warn("login de desarrollo rechazado: Host ajeno", "host", r.Host)
		http.Error(w, "Host no permitido con UI_DEV_LOGIN", http.StatusForbidden)
		return
	}
	if subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("key")), []byte(a.devKey)) != 1 {
		a.log.Warn("login de desarrollo rechazado: falta la clave o no es la del log")
		toLogin(w, r, "dev_key")
		return
	}
	login, ok := a.savePerson(r.Context(), store.Person{Login: devLogin, Name: devName, Provider: store.LoginDev, Subject: devLogin})
	if !ok {
		toLogin(w, r, "save")
		return
	}
	if err := a.setSession(w, login, devName); err != nil {
		http.Error(w, "error interno", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, safeNext(r.URL.Query().Get("next")), http.StatusFound)
}

// handleLogout borra la cookie y revoca en el servidor todas las sesiones
// de la persona emitidas hasta ahora, también las de otros navegadores.
func (a *Auth) handleLogout(w http.ResponseWriter, r *http.Request) {
	if a.enabled() && !a.sameOrigin(r) {
		http.Error(w, "Origin no es el de PUBLIC_URL", http.StatusForbidden)
		return
	}
	http.SetCookie(w, a.cookie(a.sessionName(), "", "/", -1))
	s, ok, err := a.checkSession(r)
	if err != nil {
		// Sin poder comprobarla no se puede revocar: decirlo, para que no
		// parezca cerrada mientras una copia de la cookie sigue valiendo.
		http.Error(w, "no se ha podido cerrar la sesión en el servidor", http.StatusInternalServerError)
		return
	}
	if ok {
		if _, err := a.people.RevokeSessions(r.Context(), s.Login, a.cfg.Now()); err != nil {
			a.log.Error("logout: revocar la sesión", "login", s.Login, "err", err)
			http.Error(w, "error interno", http.StatusInternalServerError)
			return
		}
		a.log.Info("logout", "login", s.Login)
	}
	w.WriteHeader(http.StatusNoContent)
}
