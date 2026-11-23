// Package webauth es el login de la interfaz: GitHub, limitado a los miembros
// activos de una org, un proveedor OIDC, limitado por dominio del correo o por
// grupo, o el modo de desarrollo, con la sesión en una cookie firmada con HMAC.
package webauth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/intake-es/intake-errors/internal/api"
	"github.com/intake-es/intake-errors/internal/store"
)

const (
	sessionCookie = "ie_session"
	stateCookie   = "ie_oauth_state"
	sessionTTL    = 7 * 24 * time.Hour
	stateTTL      = 10 * time.Minute
	devLogin      = "dev"
	devName       = "Modo desarrollo"
)

type Config struct {
	ClientID     string
	ClientSecret string
	Org          string
	// SessionSecret firma las cookies. Obligatorio con GitHub; en desarrollo,
	// si falta, se genera uno al arrancar.
	SessionSecret string
	PublicURL     string
	ListenAddr    string
	DevLogin      bool
	OIDC          OIDCConfig

	// GitHubURL y GitHubAPIURL apuntan a github.com por defecto; los tests los cambian.
	GitHubURL    string
	GitHubAPIURL string
	HTTPClient   *http.Client
	Now          func() time.Time
}

// LoadConfig lee GITHUB_CLIENT_ID, GITHUB_CLIENT_SECRET, GITHUB_ORG,
// SESSION_SECRET, UI_DEV_LOGIN y las OIDC_*.
func LoadConfig(getenv func(string) string, listenAddr, publicURL string) Config {
	dev := strings.TrimSpace(getenv("UI_DEV_LOGIN"))
	return Config{
		OIDC:          loadOIDCConfig(getenv),
		ClientID:      strings.TrimSpace(getenv("GITHUB_CLIENT_ID")),
		ClientSecret:  strings.TrimSpace(getenv("GITHUB_CLIENT_SECRET")),
		Org:           strings.TrimSpace(getenv("GITHUB_ORG")),
		SessionSecret: getenv("SESSION_SECRET"),
		PublicURL:     publicURL,
		ListenAddr:    listenAddr,
		DevLogin:      dev == "1" || strings.EqualFold(dev, "true"),
	}
}

type Auth struct {
	cfg    Config
	log    *slog.Logger
	github bool
	secret []byte
	// origins son los valores que puede traer Origin: el de PUBLIC_URL o, en
	// desarrollo sin PUBLIC_URL, 127.0.0.1 y localhost con el puerto de LISTEN_ADDR.
	origins []string
	secure  bool
	people  PeopleSaver
	oidc    *oidcProvider
}

// PeopleSaver guarda a quien entra, para poder asignarle issues, y devuelve
// con qué login se la nombra.
type PeopleSaver interface {
	SaveIdentity(ctx context.Context, p store.Person) (string, error)
}

// SavePeople hace que cada login guarde a la persona en people.
func (a *Auth) SavePeople(people PeopleSaver) { a.people = people }

// savePerson devuelve el login de la sesión. Si no se puede guardar, con
// GitHub o en desarrollo se entra igual con el login de siempre (solo se
// pierde poder elegirla al asignar), salvo que haya OIDC: entonces ese login
// podría ser el de otra persona y no se entra.
func (a *Auth) savePerson(ctx context.Context, p store.Person) (string, bool) {
	if a.people == nil {
		return p.Login, true
	}
	login, err := a.people.SaveIdentity(ctx, p)
	if err != nil {
		a.log.Error("login: guardar la persona", "provider", p.Provider, "login", p.Login, "err", err)
		return p.Login, a.oidc == nil
	}
	return login, true
}

// New valida la configuración. Sin GitHub ni modo de desarrollo, la interfaz
// dice que el login no está configurado y solo valen los tokens.
func New(cfg Config, log *slog.Logger) (*Auth, error) {
	if cfg.GitHubURL == "" {
		cfg.GitHubURL = "https://github.com"
	}
	if cfg.GitHubAPIURL == "" {
		cfg.GitHubAPIURL = "https://api.github.com"
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 10 * time.Second}
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	a := &Auth{cfg: cfg, log: log}

	set := 0
	for _, v := range []string{cfg.ClientID, cfg.ClientSecret, cfg.Org} {
		if v != "" {
			set++
		}
	}
	switch set {
	case 0:
	case 3:
		a.github = true
	default:
		return nil, errors.New("el login con GitHub necesita GITHUB_CLIENT_ID, GITHUB_CLIENT_SECRET y GITHUB_ORG juntas")
	}

	if cfg.OIDC.configured() {
		p, err := newOIDCProvider(cfg.OIDC, cfg.HTTPClient, cfg.Now)
		if err != nil {
			return nil, err
		}
		a.oidc = p
	}

	if cfg.DevLogin && !loopback(cfg.ListenAddr) {
		return nil, fmt.Errorf("UI_DEV_LOGIN=1 solo vale con LISTEN_ADDR en 127.0.0.1 o localhost, no en %q", cfg.ListenAddr)
	}
	if !a.enabled() {
		return a, nil
	}

	public := cfg.PublicURL
	if public == "" && !a.github && a.oidc == nil {
		public = "http://" + cfg.ListenAddr
	}
	u, err := url.Parse(public)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, errors.New("el login de la interfaz necesita PUBLIC_URL, una URL http o https absoluta")
	}
	a.origins = []string{strings.ToLower(u.Scheme + "://" + u.Host)}
	if cfg.PublicURL == "" {
		_, port, _ := net.SplitHostPort(cfg.ListenAddr)
		a.origins = []string{"http://127.0.0.1:" + port, "http://localhost:" + port}
	}
	a.secure = u.Scheme == "https"
	a.cfg.PublicURL = strings.TrimRight(public, "/")

	switch {
	case len(cfg.SessionSecret) >= 32:
		a.secret = []byte(cfg.SessionSecret)
	case a.github:
		return nil, errors.New("el login con GitHub necesita SESSION_SECRET, de 32 caracteres o más")
	case a.oidc != nil:
		return nil, errors.New("el login con OIDC necesita SESSION_SECRET, de 32 caracteres o más")
	case cfg.SessionSecret != "":
		return nil, errors.New("SESSION_SECRET debe tener 32 caracteres o más")
	default:
		a.secret = make([]byte, 32)
		if _, err := rand.Read(a.secret); err != nil {
			return nil, err
		}
	}
	if cfg.DevLogin {
		log.Warn("UI_DEV_LOGIN=1: la interfaz deja entrar como dev sin GitHub", "addr", cfg.ListenAddr)
	}
	if a.oidc != nil && a.oidc.cfg.AllowAny {
		log.Warn("OIDC_ALLOW_ANY=1: entra cualquiera con cuenta en el emisor", "issuer", a.oidc.cfg.Issuer)
	}
	return a, nil
}

func loopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	return host == "127.0.0.1" || host == "localhost"
}

func (a *Auth) methods() []string {
	m := []string{}
	if a.github {
		m = append(m, "github")
	}
	if a.oidc != nil {
		m = append(m, "oidc")
	}
	if a.cfg.DevLogin {
		m = append(m, "dev")
	}
	return m
}

func (a *Auth) enabled() bool { return a.github || a.oidc != nil || a.cfg.DevLogin }

// Authenticator reconoce la cookie de sesión. En las peticiones que cambian
// algo exige además que Origin sea el de PUBLIC_URL.
func (a *Auth) Authenticator() api.Authenticator {
	return func(r *http.Request) (api.Principal, bool, error) {
		s, ok := a.session(r)
		if !ok {
			return api.Principal{}, false, nil
		}
		if !safeMethod(r.Method) && !a.sameOrigin(r) {
			return api.Principal{}, false, fmt.Errorf("%w: Origin no es el de PUBLIC_URL", api.ErrForbidden)
		}
		return api.Principal{Kind: "session", Name: s.Login}, true, nil
	}
}

func safeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}

func (a *Auth) sameOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	for _, want := range a.origins {
		if o != "" && strings.EqualFold(o, want) {
			return true
		}
	}
	return false
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// safeNext deja volver solo a una ruta de este mismo servicio.
func safeNext(next string) string {
	if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.ContainsAny(next, "\\\r\n") {
		return "/"
	}
	u, err := url.Parse(next)
	if err != nil || u.Scheme != "" || u.Host != "" || strings.HasPrefix(u.Path, "/auth/") {
		return "/"
	}
	return next
}
