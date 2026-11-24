package webauth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/intake-es/mothlog/internal/store"
)

// Session es lo que lleva la cookie. Caduca sola a los 7 días, y antes si
// IssuedAt no es posterior al sessions_valid_since de la persona.
type Session struct {
	Login string `json:"l"`
	Name  string `json:"n,omitempty"`
	// IssuedAt es cuándo se emitió, en microsegundos desde 1970.
	IssuedAt int64 `json:"i"`
	Expires  int64 `json:"e"`
}

type oauthState struct {
	State   string `json:"s"`
	Next    string `json:"n"`
	Expires int64  `json:"e"`
}

// sign devuelve payload.firma, en base64url. purpose entra en la firma para
// que una cookie de un tipo no valga como la del otro.
func (a *Auth) sign(purpose string, v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	payload := base64.RawURLEncoding.EncodeToString(b)
	return payload + "." + base64.RawURLEncoding.EncodeToString(a.mac(purpose, payload)), nil
}

func (a *Auth) mac(purpose, payload string) []byte {
	m := hmac.New(sha256.New, a.secret)
	m.Write([]byte(purpose + "\x00" + payload))
	return m.Sum(nil)
}

func (a *Auth) verify(purpose, value string, v any) bool {
	payload, sig, ok := strings.Cut(value, ".")
	if !ok {
		return false
	}
	got, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil || !hmac.Equal(got, a.mac(purpose, payload)) {
		return false
	}
	b, err := base64.RawURLEncoding.DecodeString(payload)
	return err == nil && json.Unmarshal(b, v) == nil
}

// sessionName lleva el prefijo __Host- con PUBLIC_URL https: así el
// navegador solo la acepta con Secure, Path=/ y sin Domain.
func (a *Auth) sessionName() string {
	if a.secure {
		return "__Host-" + sessionCookie
	}
	return sessionCookie
}

// session devuelve la sesión de la petición si la firma es buena, no ha
// caducado y no se ha revocado. Si no se puede consultar la revocación, no
// hay sesión.
func (a *Auth) session(r *http.Request) (Session, bool) {
	s, ok, _ := a.checkSession(r)
	return s, ok
}

// checkSession es session, con el error si no se pudo consultar la revocación.
func (a *Auth) checkSession(r *http.Request) (Session, bool, error) {
	if !a.enabled() || a.people == nil || !a.hostAllowed(r) {
		return Session{}, false, nil
	}
	c, err := r.Cookie(a.sessionName())
	if err != nil {
		return Session{}, false, nil
	}
	var s Session
	if !a.verify("session", c.Value, &s) || s.Login == "" || s.IssuedAt <= 0 || a.cfg.Now().Unix() >= s.Expires {
		return Session{}, false, nil
	}
	since, err := a.people.SessionsValidSince(r.Context(), s.Login)
	if errors.Is(err, store.ErrNotFound) {
		return Session{}, false, nil
	}
	if err != nil {
		a.log.Error("session: check whether revoked", "login", s.Login, "err", err)
		return Session{}, false, err
	}
	if s.IssuedAt <= since.UnixMicro() {
		return Session{}, false, nil
	}
	return s, true, nil
}

func (a *Auth) setSession(w http.ResponseWriter, login, name string) error {
	now := a.cfg.Now()
	v, err := a.sign("session", Session{Login: login, Name: name, IssuedAt: now.UnixMicro(), Expires: now.Add(sessionTTL).Unix()})
	if err != nil {
		return err
	}
	http.SetCookie(w, a.cookie(a.sessionName(), v, "/", sessionTTL))
	return nil
}

func (a *Auth) cookie(name, value, path string, ttl time.Duration) *http.Cookie {
	c := &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     path,
		HttpOnly: true,
		Secure:   a.secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(ttl.Seconds()),
	}
	if ttl < 0 {
		c.MaxAge = -1
		c.Value = ""
	}
	return c
}
