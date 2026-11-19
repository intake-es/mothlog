package envelope

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
)

var ErrNoPublicKey = errors.New("falta la clave pública del DSN")

// PublicKey saca la clave pública de X-Sentry-Auth (SDK de servidor) o de
// sentry_key en la query (navegador).
func PublicKey(h http.Header, q url.Values) (string, error) {
	for _, v := range []string{h.Get("X-Sentry-Auth"), h.Get("Authorization")} {
		if k := authKey(v); k != "" {
			return k, nil
		}
	}
	if k := strings.TrimSpace(q.Get("sentry_key")); k != "" {
		return k, nil
	}
	return "", ErrNoPublicKey
}

// authKey lee «Sentry sentry_key=…, sentry_version=7, …».
func authKey(v string) string {
	v = strings.TrimSpace(v)
	scheme, params, ok := strings.Cut(v, " ")
	if !ok || !strings.EqualFold(scheme, "Sentry") {
		return ""
	}
	for p := range strings.SplitSeq(params, ",") {
		k, val, ok := strings.Cut(strings.TrimSpace(p), "=")
		if ok && strings.TrimSpace(k) == "sentry_key" {
			return strings.Trim(strings.TrimSpace(val), `"`)
		}
	}
	return ""
}
