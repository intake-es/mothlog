package webauth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// Session es lo que lleva la cookie. No hay tabla: caduca sola a los 7 días.
type Session struct {
	Login   string `json:"l"`
	Name    string `json:"n,omitempty"`
	Expires int64  `json:"e"`
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

func (a *Auth) session(r *http.Request) (Session, bool) {
	if !a.enabled() {
		return Session{}, false
	}
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return Session{}, false
	}
	var s Session
	if !a.verify("session", c.Value, &s) || s.Login == "" || a.cfg.Now().Unix() >= s.Expires {
		return Session{}, false
	}
	return s, true
}

func (a *Auth) setSession(w http.ResponseWriter, login, name string) error {
	exp := a.cfg.Now().Add(sessionTTL)
	v, err := a.sign("session", Session{Login: login, Name: name, Expires: exp.Unix()})
	if err != nil {
		return err
	}
	http.SetCookie(w, a.cookie(sessionCookie, v, "/", sessionTTL))
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
