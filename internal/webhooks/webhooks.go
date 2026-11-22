// Package webhooks recibe los webhooks de Linear y GitHub: lo que se cierra o
// se reabre allí cambia el estado de los errores enlazados, y un PR mergeado
// que enlaza un error lo deja resuelto en la siguiente versión.
package webhooks

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/intake-es/intake-errors/internal/store"
)

const (
	LinearPath = "/webhooks/linear"
	GitHubPath = "/webhooks/github"

	// MaxAge es lo más vieja que puede llegar una entrega de Linear, como
	// recomienda Linear.
	MaxAge = time.Minute

	maxBody = 5 << 20
)

type Options struct {
	LinearSecret string
	GitHubSecret string
	// PublicURL es la base de los enlaces a los errores que se buscan en los
	// PRs; vacía, la de la petición.
	PublicURL string
	// LinearAPIKey y GitHubToken solo dicen, en Status, si se puede escribir allí.
	LinearAPIKey bool
	GitHubToken  bool
	Now          func() time.Time
	Log          *slog.Logger
}

type Handler struct {
	st   *store.Store
	opts Options
}

func New(st *store.Store, o Options) *Handler {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	o.PublicURL = strings.TrimRight(o.PublicURL, "/")
	return &Handler{st: st, opts: o}
}

// Register añade las dos rutas, sin token: las protege la firma.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST "+LinearPath, h.linear)
	mux.HandleFunc("POST "+GitHubPath, h.github)
}

// ProviderStatus es lo que la interfaz enseña de cada integración.
type ProviderStatus struct {
	WebhookURL string `json:"webhook_url"`
	// SecretSet: el servidor tiene el secreto con el que comprobar las firmas.
	SecretSet bool `json:"secret_set"`
	// TokenSet: el servidor puede escribir allí (LINEAR_API_KEY o GITHUB_TOKEN).
	TokenSet bool `json:"token_set"`
	// LastDelivery es la última entrega con buena firma; nil, ninguna.
	LastDelivery *time.Time `json:"last_delivery"`
}

type Status struct {
	Linear ProviderStatus `json:"linear"`
	GitHub ProviderStatus `json:"github"`
}

// Status dice cómo están las dos integraciones. base es la URL pública del
// servicio si no hay PUBLIC_URL.
func (h *Handler) Status(ctx context.Context, base string) (Status, error) {
	if h.opts.PublicURL != "" {
		base = h.opts.PublicURL
	}
	base = strings.TrimRight(base, "/")
	s := Status{
		Linear: ProviderStatus{WebhookURL: base + LinearPath, SecretSet: h.opts.LinearSecret != "", TokenSet: h.opts.LinearAPIKey},
		GitHub: ProviderStatus{WebhookURL: base + GitHubPath, SecretSet: h.opts.GitHubSecret != "", TokenSet: h.opts.GitHubToken},
	}
	var err error
	if s.Linear.LastDelivery, err = h.st.LastWebhookDelivery(ctx, store.ProviderLinear); err != nil {
		return s, err
	}
	s.GitHub.LastDelivery, err = h.st.LastWebhookDelivery(ctx, store.ProviderGitHub)
	return s, err
}

var errSignature = errors.New("firma no válida")

// verify comprueba que sig (hexadecimal) es el HMAC-SHA256 de body con secret.
func verify(secret string, body []byte, sig string) error {
	got, err := hex.DecodeString(strings.TrimSpace(sig))
	if err != nil || len(got) != sha256.Size {
		return errSignature
	}
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(body)
	if !hmac.Equal(got, m.Sum(nil)) {
		return errSignature
	}
	return nil
}

// Sign firma body como Linear y GitHub; lo usan los tests y los servidores falsos.
func Sign(secret string, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(body)
	return hex.EncodeToString(m.Sum(nil))
}

func readBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		http.Error(w, "cuerpo demasiado grande o cortado", http.StatusBadRequest)
		return nil, false
	}
	return body, true
}

func reply(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(code)
	_, _ = fmt.Fprintln(w, msg)
}

// apply pone en status los errores enlazados y lo registra.
func (h *Handler) apply(ctx context.Context, issueID int64, status string, by store.ExternalChange) error {
	changed, err := h.st.ApplyExternalStatus(ctx, issueID, status, by)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if changed {
		h.opts.Log.InfoContext(ctx, "estado cambiado desde fuera", "origen", by.Source, "issue", issueID, "estado", status)
	}
	return nil
}

// failed responde con un error para que el sitio reintente la entrega, y la
// olvida para aceptar el reintento.
func (h *Handler) failed(w http.ResponseWriter, r *http.Request, provider, delivery string, err error) {
	h.opts.Log.ErrorContext(r.Context(), "webhook", "origen", provider, "entrega", delivery, "err", err)
	if ferr := h.st.ForgetWebhookDelivery(context.WithoutCancel(r.Context()), provider, delivery); ferr != nil {
		h.opts.Log.ErrorContext(r.Context(), "webhook", "origen", provider, "err", ferr)
	}
	reply(w, http.StatusInternalServerError, "error interno")
}

// base es la URL de los errores en el panel: PUBLIC_URL o la de la petición.
func (h *Handler) base(r *http.Request) *url.URL {
	if h.opts.PublicURL != "" {
		if u, err := url.Parse(h.opts.PublicURL); err == nil && u.Host != "" {
			return u
		}
	}
	u := &url.URL{Scheme: "http", Host: r.Host}
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		u.Scheme = "https"
	}
	return u
}
