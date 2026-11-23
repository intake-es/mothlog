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
	"path"
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
	// desarrollo sin PUBLIC_URL, 127.0.0.1, localhost y [::1] con el puerto de LISTEN_ADDR.
	origins []string
	// devHosts son los Host que valen con UI_DEV_LOGIN: los de loopback con
	// el puerto de LISTEN_ADDR y el de PUBLIC_URL.
	devHosts []string
	devKey   string
	secure   bool
	people   People
	oidc     *oidcProvider
}

// People guarda a quien entra, para poder asignarle issues, y sabe desde
// cuándo valen sus sesiones.
type People interface {
	// SaveIdentity devuelve con qué login se nombra a la persona.
	SaveIdentity(ctx context.Context, p store.Person) (string, error)
	// SessionsValidSince es store.ErrNotFound si no hay nadie con ese login.
	SessionsValidSince(ctx context.Context, login string) (time.Time, error)
	RevokeSessions(ctx context.Context, login string, at time.Time) (string, error)
	// RenamePerson cambia el login from por base (o base~N) y devuelve el nuevo.
	RenamePerson(ctx context.Context, from, base string) (string, error)
}

// SavePeople conecta la tabla people: sin ella no hay sesiones.
func (a *Auth) SavePeople(people People) { a.people = people }

// savePerson devuelve el login de la sesión. Si no se puede guardar a la
// persona, no se entra: sin fila no se podría revocar su sesión.
func (a *Auth) savePerson(ctx context.Context, p store.Person) (string, bool) {
	if a.people == nil {
		a.log.Error("login: no hay dónde guardar a la persona", "provider", p.Provider, "login", p.Login)
		return "", false
	}
	login, err := a.people.SaveIdentity(ctx, p)
	if err != nil {
		a.log.Error("login: guardar la persona", "provider", p.Provider, "login", p.Login, "err", err)
		return "", false
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

	if cfg.DevLogin && !Loopback(cfg.ListenAddr) {
		return nil, fmt.Errorf("UI_DEV_LOGIN=1 solo vale con LISTEN_ADDR en 127.0.0.1, [::1] o localhost, no en %q", cfg.ListenAddr)
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
	if (a.github || a.oidc != nil) && u.Scheme != "https" && !loopbackHost(u.Hostname()) {
		return nil, errors.New("con login de GitHub u OIDC, PUBLIC_URL tiene que ser https (http solo en localhost)")
	}
	a.origins = []string{strings.ToLower(u.Scheme + "://" + u.Host)}
	_, port, _ := net.SplitHostPort(cfg.ListenAddr)
	if cfg.PublicURL == "" {
		a.origins = nil
		for _, h := range loopbackHosts(port) {
			a.origins = append(a.origins, "http://"+h)
		}
	}
	if cfg.DevLogin {
		a.devHosts = loopbackHosts(port)
		if cfg.PublicURL != "" {
			a.devHosts = append(a.devHosts, strings.ToLower(u.Host))
		}
		a.devKey = randomHex(32)
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
		log.Warn("UI_DEV_LOGIN=1: se entra como dev con este enlace, que no se vuelve a mostrar",
			"url", a.cfg.PublicURL+"/auth/dev/login?key="+a.devKey)
	}
	if a.oidc != nil && a.oidc.cfg.AllowAny {
		log.Warn("OIDC_ALLOW_ANY=1: entra cualquiera con cuenta en el emisor", "issuer", a.oidc.cfg.Issuer)
	}
	return a, nil
}

// Loopback dice si addr, de la forma host:puerto, escucha solo en loopback:
// localhost, 127.0.0.0/8 o ::1.
func Loopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	return err == nil && loopbackHost(host)
}

func loopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// loopbackHosts son los Host con los que se llega a loopback en ese puerto.
// En el 80 el navegador no manda el puerto.
func loopbackHosts(port string) []string {
	hosts := []string{"localhost:" + port, "127.0.0.1:" + port, "[::1]:" + port}
	if port == "80" {
		hosts = append(hosts, "localhost", "127.0.0.1", "[::1]")
	}
	return hosts
}

// hostAllowed: con UI_DEV_LOGIN, el Host tiene que ser uno de los de
// loopback o el de PUBLIC_URL. Así una página con DNS rebinding, que llega
// con su propio nombre, no entra ni lee con la sesión.
func (a *Auth) hostAllowed(r *http.Request) bool {
	if !a.cfg.DevLogin {
		return true
	}
	host := strings.ToLower(r.Host)
	for _, h := range a.devHosts {
		if host == h {
			return true
		}
	}
	return false
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

// safeNext deja volver solo a una ruta de este mismo servicio y fuera de
// /auth/. El path se mira desescapado y tiene que estar ya limpio: con ., ..
// o // (también escritos con %) podría resolverse en otra ruta.
func safeNext(next string) string {
	if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.ContainsAny(next, "\\\r\n") {
		return "/"
	}
	u, err := url.Parse(next)
	if err != nil || u.Scheme != "" || u.Host != "" {
		return "/"
	}
	clean := path.Clean(u.Path)
	if u.Path != "/" && strings.HasSuffix(u.Path, "/") {
		clean += "/"
	}
	if clean != u.Path || clean == "/auth" || strings.HasPrefix(clean, "/auth/") {
		return "/"
	}
	return next
}
