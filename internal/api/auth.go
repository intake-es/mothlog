package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/intake-es/intake-errors/internal/store"
)

// Principal es quien hace la petición.
type Principal struct {
	Kind string // "token" o "session"
	Name string
}

// Authenticator reconoce una forma de autenticarse. Devuelve ok=false si la
// petición no la trae, para que pruebe la siguiente; un error es un 401.
type Authenticator func(r *http.Request) (p Principal, ok bool, err error)

var errBadToken = errors.New("token no válido o revocado")

// ErrForbidden envuelve el error de un Authenticator que reconoce la petición
// pero no la acepta: es un 403 con su mensaje.
var ErrForbidden = errors.New("prohibido")

// BearerToken autentica con `Authorization: Bearer ie_…`.
func BearerToken(st *store.Store) Authenticator {
	return func(r *http.Request) (Principal, bool, error) {
		h := r.Header.Get("Authorization")
		if h == "" {
			return Principal{}, false, nil
		}
		scheme, secret, _ := strings.Cut(h, " ")
		if !strings.EqualFold(scheme, "Bearer") || strings.TrimSpace(secret) == "" {
			return Principal{}, false, errBadToken
		}
		t, err := st.Authenticate(r.Context(), strings.TrimSpace(secret))
		if errors.Is(err, store.ErrNotFound) {
			return Principal{}, false, errBadToken
		}
		if err != nil {
			return Principal{}, false, err
		}
		return Principal{Kind: "token", Name: t.Name}, true, nil
	}
}

type principalKey struct{}

func PrincipalFrom(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}

// RequireAuth deja pasar la petición si alguno de auths la reconoce. Lo que
// pasa por aquí depende de quién lo pide: nadie lo guarda en caché.
func RequireAuth(auths ...Authenticator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			for _, auth := range auths {
				p, ok, err := auth(r)
				if errors.Is(err, errBadToken) {
					unauthorized(w, err.Error())
					return
				}
				if errors.Is(err, ErrForbidden) {
					writeError(w, http.StatusForbidden, err.Error())
					return
				}
				if err != nil {
					slog.ErrorContext(r.Context(), "autenticar", "err", err)
					writeError(w, http.StatusInternalServerError, "error interno")
					return
				}
				if ok {
					next.ServeHTTP(&noStoreWriter{ResponseWriter: w}, r.WithContext(context.WithValue(r.Context(), principalKey{}, p)))
					return
				}
			}
			unauthorized(w, "falta Authorization: Bearer <token>")
		})
	}
}

// noStoreWriter deja Cache-Control: no-store aunque el handler ponga otro,
// como hace el MCP con sus respuestas en streaming.
type noStoreWriter struct {
	http.ResponseWriter
	wrote bool
}

func (w *noStoreWriter) WriteHeader(code int) {
	if !w.wrote {
		w.wrote = true
		w.Header().Set("Cache-Control", "no-store")
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *noStoreWriter) Write(b []byte) (int, error) {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(b)
}

func (w *noStoreWriter) Flush() {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	_ = http.NewResponseController(w.ResponseWriter).Flush()
}

func (w *noStoreWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func unauthorized(w http.ResponseWriter, msg string) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="intake-errors"`)
	writeError(w, http.StatusUnauthorized, msg)
}
