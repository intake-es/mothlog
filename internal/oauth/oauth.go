// Package oauth es el servidor de autorización OAuth 2.1 del MCP, como lo pide
// la especificación de MCP: descubrimiento (RFC 9728 y RFC 8414), registro
// dinámico (RFC 7591) y Client ID Metadata Documents, código de autorización
// con PKCE S256 y resource (RFC 8707), refresco con rotación y revocación
// (RFC 7009). El consentimiento lo da una persona con su sesión del panel.
package oauth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/intake-es/mothlog/internal/api"
	"github.com/intake-es/mothlog/internal/mcpserver"
	"github.com/intake-es/mothlog/internal/store"
)

const (
	requestTTL = 10 * time.Minute
	codeTTL    = 2 * time.Minute
	pruneEvery = time.Hour
)

// DefaultTTL son las vidas de los tokens: una hora el de acceso y 30 días el
// de refresco, que cambia en cada uso.
var DefaultTTL = store.OAuthTTL{Access: time.Hour, Refresh: 30 * 24 * time.Hour}

var scopes = []string{mcpserver.ScopeRead, mcpserver.ScopeWrite}

type Config struct {
	// PublicURL es el emisor y la base de las URLs; sin ella no hay OAuth.
	PublicURL string
	Log       *slog.Logger
	// HTTPClient pide los Client ID Metadata Documents; por defecto, uno que
	// no pide a direcciones privadas ni sigue redirecciones.
	HTTPClient *http.Client
	Now        func() time.Time
	TTL        store.OAuthTTL
}

type Server struct {
	st       *store.Store
	cfg      Config
	issuer   string
	resource string
	log      *slog.Logger
	http     *http.Client
	// registers y authorizations limitan lo que puede pedir cada dirección.
	registers      *limiter
	authorizations *limiter
}

// New devuelve nil si no hay PUBLIC_URL: sin ella no se ofrece OAuth.
func New(st *store.Store, cfg Config) (*Server, error) {
	if cfg.PublicURL == "" {
		return nil, nil
	}
	u, err := url.Parse(cfg.PublicURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("MCP OAuth: PUBLIC_URL must be an absolute http or https URL")
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.TTL == (store.OAuthTTL{}) {
		cfg.TTL = DefaultTTL
	}
	client := cfg.HTTPClient
	if client == nil {
		client = metadataClient()
	}
	issuer := strings.TrimRight(cfg.PublicURL, "/")
	return &Server{st: st, cfg: cfg, issuer: issuer, resource: issuer + "/mcp", log: cfg.Log, http: client,
		registers: newLimiter(maxRegistersPerIP), authorizations: newLimiter(maxAuthorizePerIP)}, nil
}

// Resource es la URL del recurso protegido, el MCP.
func (s *Server) Resource() string { return s.resource }

func (s *Server) metadataURL() string {
	return s.issuer + "/.well-known/oauth-protected-resource/mcp"
}

// Challenge es el WWW-Authenticate de un 401 de /mcp, que dice dónde está la
// metadata del recurso (RFC 9728, sección 5.1).
func (s *Server) Challenge(badToken bool) string {
	c := `Bearer realm="mothlog", resource_metadata="` + s.metadataURL() + `", scope="` + strings.Join(scopes, " ") + `"`
	if badToken {
		c += `, error="invalid_token"`
	}
	return c
}

// Register añade el descubrimiento y los endpoints de OAuth. Lo del panel
// (consentimiento y aplicaciones conectadas) va con session, que solo
// reconoce la sesión del panel.
func (s *Server) Register(mux *http.ServeMux, session func(http.Handler) http.Handler) {
	mux.HandleFunc("GET /.well-known/oauth-protected-resource", s.protectedResource)
	mux.HandleFunc("GET /.well-known/oauth-protected-resource/mcp", s.protectedResource)
	mux.HandleFunc("GET /.well-known/oauth-authorization-server", s.authorizationServer)
	mux.HandleFunc("POST /oauth/register", s.register)
	mux.HandleFunc("GET /oauth/authorize", s.authorize)
	mux.HandleFunc("POST /oauth/token", s.token)
	mux.HandleFunc("POST /oauth/revoke", s.revoke)
	for _, p := range []string{"/.well-known/oauth-protected-resource", "/.well-known/oauth-protected-resource/mcp",
		"/.well-known/oauth-authorization-server", "/oauth/register", "/oauth/token", "/oauth/revoke"} {
		mux.HandleFunc("OPTIONS "+p, preflight)
	}

	route := func(pattern string, f http.HandlerFunc) { mux.Handle(pattern, session(onlySession(f))) }
	route("GET /api/0/oauth/requests/{id}", s.pendingRequest)
	route("POST /api/0/oauth/requests/{id}/approve", s.approve)
	route("POST /api/0/oauth/requests/{id}/deny", s.deny)
	route("GET /api/0/oauth/grants/{$}", s.grants)
	route("DELETE /api/0/oauth/grants/{id}", s.revokeGrant)
}

// Run limpia cada hora lo que ya no sirve.
func (s *Server) Run(ctx context.Context) {
	t := time.NewTicker(pruneEvery)
	defer t.Stop()
	for {
		if err := s.st.PruneOAuth(ctx, s.cfg.Now()); err != nil && ctx.Err() == nil {
			s.log.Error("oauth: cleanup", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Authenticator reconoce los tokens de acceso de OAuth; los demás los deja
// pasar al siguiente.
func (s *Server) Authenticator() api.Authenticator {
	return func(r *http.Request) (api.Principal, bool, error) {
		scheme, secret, _ := strings.Cut(r.Header.Get("Authorization"), " ")
		secret = strings.TrimSpace(secret)
		if !strings.EqualFold(scheme, "Bearer") || !store.IsOAuthAccess(secret) {
			return api.Principal{}, false, nil
		}
		a, err := s.st.AuthenticateOAuth(r.Context(), secret, clientIP(r), s.cfg.Now())
		if errors.Is(err, store.ErrNotFound) {
			return api.Principal{}, false, api.ErrBadToken
		}
		if err != nil {
			return api.Principal{}, false, err
		}
		if a.Resource != s.resource {
			return api.Principal{}, false, api.ErrBadToken
		}
		return api.Principal{Kind: "oauth", Name: a.Login, Via: a.ClientName, Scopes: a.Scopes}, true, nil
	}
}

func (s *Server) protectedResource(w http.ResponseWriter, _ *http.Request) {
	cors(w)
	writeJSON(w, http.StatusOK, map[string]any{
		"resource":                 s.resource,
		"resource_name":            "Mothlog",
		"authorization_servers":    []string{s.issuer},
		"scopes_supported":         scopes,
		"bearer_methods_supported": []string{"header"},
	})
}

func (s *Server) authorizationServer(w http.ResponseWriter, _ *http.Request) {
	cors(w)
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                         s.issuer,
		"authorization_endpoint":                         s.issuer + "/oauth/authorize",
		"token_endpoint":                                 s.issuer + "/oauth/token",
		"registration_endpoint":                          s.issuer + "/oauth/register",
		"revocation_endpoint":                            s.issuer + "/oauth/revoke",
		"scopes_supported":                               scopes,
		"response_types_supported":                       []string{"code"},
		"response_modes_supported":                       []string{"query"},
		"grant_types_supported":                          []string{"authorization_code", "refresh_token"},
		"token_endpoint_auth_methods_supported":          []string{"none"},
		"revocation_endpoint_auth_methods_supported":     []string{"none"},
		"code_challenge_methods_supported":               []string{"S256"},
		"client_id_metadata_document_supported":          true,
		"authorization_response_iss_parameter_supported": true,
	})
}

func onlySession(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if p, ok := api.PrincipalFrom(r.Context()); !ok || p.Kind != "session" {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "only with the panel session"})
			return
		}
		next(w, r)
	}
}

func cors(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
}

// preflight deja a los clientes que corren en un navegador llamar a los
// endpoints que no usan cookies.
func preflight(w http.ResponseWriter, _ *http.Request) {
	cors(w)
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, MCP-Protocol-Version")
	w.Header().Set("Access-Control-Max-Age", "600")
	w.WriteHeader(http.StatusNoContent)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// oauthError es la respuesta de error de RFC 6749, sección 5.2.
func oauthError(w http.ResponseWriter, code int, kind, desc string) {
	writeJSON(w, code, map[string]string{"error": kind, "error_description": desc})
}

func randomID() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// clientIP es la dirección de quien llama. Si llega por un proxy de la misma
// máquina o de la red privada, la última de X-Forwarded-For, que es la que
// añadió ese proxy.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	addr = addr.Unmap()
	if addr.IsLoopback() || addr.IsPrivate() {
		if xff := r.Header.Values("X-Forwarded-For"); len(xff) > 0 {
			parts := strings.Split(xff[len(xff)-1], ",")
			if fwd, err := netip.ParseAddr(strings.TrimSpace(parts[len(parts)-1])); err == nil {
				return fwd.Unmap().String()
			}
		}
	}
	return addr.String()
}
