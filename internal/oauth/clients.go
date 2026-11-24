package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/intake-es/intake-errors/internal/netguard"
	"github.com/intake-es/intake-errors/internal/store"
)

const (
	maxClientName    = 80
	maxRedirectURIs  = 10
	maxRedirectURI   = 2000
	maxMetadataBytes = 16 << 10
	metadataTimeout  = 5 * time.Second
	clientIDPrefix   = "iec_"
)

// client es un cliente, registrado o identificado por su URL.
type client struct {
	ID           string
	Name         string
	RedirectURIs []string
	// URL dice si el client_id es la URL de su Client ID Metadata Document.
	URL bool
}

// metadataClient pide los Client ID Metadata Documents con la misma
// protección que los avisos: nunca a direcciones privadas, de metadatos ni
// link-local, sin seguir redirecciones y con tiempo acotado.
func metadataClient() *http.Client {
	return &http.Client{
		Timeout:   metadataTimeout,
		Transport: netguard.Transport(netguard.Policy{}),
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// validRedirectURI: https a cualquier sitio, o http a loopback (RFC 8252),
// sin fragmento ni usuario.
func validRedirectURI(raw string) error {
	if raw == "" || len(raw) > maxRedirectURI {
		return errors.New("la URI de vuelta está vacía o es demasiado larga")
	}
	u, err := url.Parse(raw)
	if err != nil || !u.IsAbs() || u.Host == "" {
		return fmt.Errorf("%q no es una URL absoluta", raw)
	}
	if u.Fragment != "" || strings.Contains(raw, "#") || u.User != nil {
		return fmt.Errorf("%q no puede llevar fragmento ni usuario", raw)
	}
	switch {
	case u.Scheme == "https":
		return nil
	case u.Scheme == "http" && loopback(u.Hostname()):
		return nil
	}
	return fmt.Errorf("%q tiene que ser https, o http a 127.0.0.1, [::1] o localhost", raw)
}

func loopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// redirectMatches: la URI tiene que ser una de las registradas, letra a letra;
// con una IP de loopback puede cambiar el puerto (OAuth 2.1, sección 8.4.2).
func redirectMatches(registered []string, got string) bool {
	if slices.Contains(registered, got) {
		return true
	}
	g, err := url.Parse(got)
	if err != nil || g.Scheme != "http" || net.ParseIP(g.Hostname()) == nil || !loopback(g.Hostname()) {
		return false
	}
	for _, r := range registered {
		u, err := url.Parse(r)
		if err == nil && u.Scheme == g.Scheme && u.Hostname() == g.Hostname() && u.EscapedPath() == g.EscapedPath() &&
			u.RawQuery == g.RawQuery && u.Fragment == "" && g.Fragment == "" && g.User == nil {
			return true
		}
	}
	return false
}

// cleanName quita los caracteres de control y deja el nombre en 80 letras;
// sin nombre, el sitio de la URI de vuelta.
func cleanName(name string, redirects []string) string {
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, name)
	name = strings.Join(strings.Fields(name), " ")
	if utf8.RuneCountInString(name) > maxClientName {
		name = string([]rune(name)[:maxClientName])
	}
	if name == "" && len(redirects) > 0 {
		if u, err := url.Parse(redirects[0]); err == nil {
			name = u.Hostname()
		}
	}
	return name
}

type registration struct {
	RedirectURIs            []string `json:"redirect_uris"`
	ClientName              string   `json:"client_name"`
	GrantTypes              []string `json:"grant_types"`
	ResponseTypes           []string `json:"response_types"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
	Scope                   string   `json:"scope"`
}

func (r registration) validate() error {
	if len(r.RedirectURIs) == 0 || len(r.RedirectURIs) > maxRedirectURIs {
		return fmt.Errorf("hacen falta de 1 a %d redirect_uris", maxRedirectURIs)
	}
	for _, u := range r.RedirectURIs {
		if err := validRedirectURI(u); err != nil {
			return err
		}
	}
	for _, g := range r.GrantTypes {
		if g != "authorization_code" && g != "refresh_token" {
			return fmt.Errorf("grant_type %q no está admitido", g)
		}
	}
	for _, t := range r.ResponseTypes {
		if t != "code" {
			return fmt.Errorf("response_type %q no está admitido", t)
		}
	}
	return nil
}

// register es el registro dinámico (RFC 7591): clientes públicos, sin
// secreto. Lo que no llega a usarse caduca a las 24 horas.
func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	cors(w)
	if !s.registers.allow(clientIP(r), s.cfg.Now()) {
		w.Header().Set("Retry-After", strconv.Itoa(int(limitWindow.Seconds())))
		oauthError(w, http.StatusTooManyRequests, "temporarily_unavailable", "demasiados registros desde esta dirección; vuelve a probar en unos minutos")
		return
	}
	var in registration
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	if err := dec.Decode(&in); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_client_metadata", "el cuerpo tiene que ser un JSON")
		return
	}
	if err := in.validate(); err != nil {
		code := "invalid_client_metadata"
		if len(in.RedirectURIs) > 0 {
			code = "invalid_redirect_uri"
		}
		oauthError(w, http.StatusBadRequest, code, err.Error())
		return
	}
	now := s.cfg.Now()
	c := store.OAuthClient{ID: clientIDPrefix + randomID(), Name: cleanName(in.ClientName, in.RedirectURIs), RedirectURIs: in.RedirectURIs}
	if err := s.st.CreateOAuthClient(r.Context(), c, now); err != nil {
		if errors.Is(err, store.ErrOAuthTooMany) {
			oauthError(w, http.StatusTooManyRequests, "temporarily_unavailable", err.Error())
			return
		}
		s.log.Error("oauth: registrar cliente", "err", err)
		oauthError(w, http.StatusInternalServerError, "server_error", "error interno")
		return
	}
	s.log.Info("oauth: cliente registrado", "client_id", c.ID, "name", c.Name)
	writeJSON(w, http.StatusCreated, map[string]any{
		"client_id":                  c.ID,
		"client_id_issued_at":        now.Unix(),
		"client_name":                c.Name,
		"redirect_uris":              c.RedirectURIs,
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": "none",
	})
}

// metadataURL: un client_id es la URL de su documento si es https, con
// ruta y sin fragmento, usuario ni segmentos . o ..
func metadataURL(id string) (*url.URL, bool) {
	if !strings.HasPrefix(id, "https://") {
		return nil, false
	}
	u, err := url.Parse(id)
	if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" || strings.Contains(id, "#") ||
		u.Path == "" || u.Path == "/" || path.Clean(u.Path) != u.Path {
		return nil, false
	}
	return u, true
}

// errClient: el client_id no existe o su documento no vale.
var errClient = errors.New("cliente desconocido")

func (s *Server) lookupClient(ctx context.Context, id string) (client, error) {
	if strings.HasPrefix(id, "https://") {
		return s.fetchMetadata(ctx, id)
	}
	c, err := s.st.OAuthClientByID(ctx, id, s.cfg.Now())
	if errors.Is(err, store.ErrNotFound) {
		return client{}, errClient
	}
	if err != nil {
		return client{}, err
	}
	return client{ID: c.ID, Name: c.Name, RedirectURIs: c.RedirectURIs}, nil
}

// fetchMetadata pide el Client ID Metadata Document del client_id.
func (s *Server) fetchMetadata(ctx context.Context, id string) (client, error) {
	u, ok := metadataURL(id)
	if !ok {
		return client{}, fmt.Errorf("%w: el client_id %q no es una URL https válida", errClient, id)
	}
	ctx, cancel := context.WithTimeout(ctx, metadataTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return client{}, fmt.Errorf("%w: %v", errClient, err)
	}
	req.Header.Set("Accept", "application/json")
	res, err := s.http.Do(req)
	if err != nil {
		if errors.Is(err, netguard.ErrBlocked) {
			return client{}, fmt.Errorf("%w: el documento del cliente está en una dirección privada, de metadatos o reservada", errClient)
		}
		return client{}, fmt.Errorf("%w: no se pudo pedir el documento del cliente", errClient)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return client{}, fmt.Errorf("%w: el documento del cliente respondió %d", errClient, res.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, maxMetadataBytes+1))
	if err != nil || len(body) > maxMetadataBytes {
		return client{}, fmt.Errorf("%w: el documento del cliente pasa de %d bytes o no se pudo leer", errClient, maxMetadataBytes)
	}
	var doc struct {
		registration
		ClientID string `json:"client_id"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return client{}, fmt.Errorf("%w: el documento del cliente no es un JSON", errClient)
	}
	if doc.ClientID != id {
		return client{}, fmt.Errorf("%w: el client_id del documento no es su URL", errClient)
	}
	if m := doc.TokenEndpointAuthMethod; m != "" && m != "none" {
		return client{}, fmt.Errorf("%w: solo se admiten clientes públicos (token_endpoint_auth_method none)", errClient)
	}
	if err := doc.validate(); err != nil {
		return client{}, fmt.Errorf("%w: %v", errClient, err)
	}
	name := doc.ClientName
	if name == "" {
		name = u.Hostname()
	}
	return client{ID: id, Name: cleanName(name, doc.RedirectURIs), RedirectURIs: doc.RedirectURIs, URL: true}, nil
}
