package ingest

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/intake-es/intake-errors/internal/envelope"
	"github.com/intake-es/intake-errors/internal/event"
	"github.com/intake-es/intake-errors/internal/store"
)

// Pattern es la ruta del envelope para http.ServeMux.
const Pattern = "/api/{project_id}/envelope/{$}"

const (
	MaxCompressed   = 1 << 20
	MaxDecompressed = 10 << 20
)

type Projects interface {
	ProjectByID(ctx context.Context, id int64) (store.Project, error)
}

type Handler struct {
	projects Projects
	ingester *Ingester
	limiter  *RateLimiter
	log      *slog.Logger
}

func NewHandler(projects Projects, ingester *Ingester, limiter *RateLimiter, log *slog.Logger) *Handler {
	return &Handler{projects: projects, ingester: ingester, limiter: limiter, log: log}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	hdr := w.Header()
	hdr.Set("Access-Control-Allow-Origin", "*")
	// Sin esto el SDK del navegador no puede leer los límites de la respuesta.
	hdr.Set("Access-Control-Expose-Headers", "Retry-After, X-Sentry-Rate-Limits")
	switch r.Method {
	case http.MethodOptions:
		hdr.Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		hdr.Set("Access-Control-Allow-Headers", "Content-Type, Content-Encoding, X-Sentry-Auth, Authorization, X-Requested-With, Origin, Referer, Accept, sentry-trace, baggage")
		hdr.Set("Access-Control-Max-Age", "86400")
		w.WriteHeader(http.StatusNoContent)
	case http.MethodPost:
		h.envelope(w, r)
	default:
		hdr.Set("Allow", "POST, OPTIONS")
		fail(w, http.StatusMethodNotAllowed, "método no permitido")
	}
}

func (h *Handler) envelope(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	projectID, err := strconv.ParseInt(r.PathValue("project_id"), 10, 64)
	if err != nil {
		fail(w, http.StatusNotFound, "proyecto no encontrado")
		return
	}
	project, err := h.projects.ProjectByID(ctx, projectID)
	if errors.Is(err, store.ErrNotFound) {
		fail(w, http.StatusNotFound, "proyecto no encontrado")
		return
	}
	if err != nil {
		h.log.ErrorContext(ctx, "buscar proyecto", "proyecto", projectID, "err", err)
		fail(w, http.StatusInternalServerError, "error interno")
		return
	}
	key, err := envelope.PublicKey(r.Header, r.URL.Query())
	if err != nil || subtle.ConstantTimeCompare([]byte(key), []byte(project.PublicKey)) != 1 {
		fail(w, http.StatusUnauthorized, "clave pública no válida")
		return
	}

	if r.ContentLength > MaxCompressed {
		fail(w, http.StatusRequestEntityTooLarge, "envelope demasiado grande")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxCompressed))
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			fail(w, http.StatusRequestEntityTooLarge, "envelope demasiado grande")
			return
		}
		fail(w, http.StatusBadRequest, "no se pudo leer el cuerpo")
		return
	}
	data, err := envelope.Decompress(bytes.NewReader(body), r.Header.Get("Content-Encoding"), MaxDecompressed)
	if errors.Is(err, envelope.ErrTooLarge) {
		fail(w, http.StatusRequestEntityTooLarge, "envelope demasiado grande")
		return
	}
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	env, err := envelope.Parse(data)
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}

	headerID := strings.ToLower(strings.ReplaceAll(env.Header.EventID, "-", ""))
	var events []*event.Event
	for _, it := range env.Events() {
		ev, err := event.Parse(it.Payload)
		if err != nil {
			fail(w, http.StatusBadRequest, err.Error())
			return
		}
		if ev.ID == "" {
			ev.ID = headerID
		}
		events = append(events, ev)
	}

	if len(events) > 0 {
		if ok, wait := h.limiter.Allow(project.ID, len(events)); !ok {
			secs := strconv.Itoa(max(1, int(math.Ceil(wait.Seconds()))))
			hdr := w.Header()
			hdr.Set("Retry-After", secs)
			hdr.Set("X-Sentry-Rate-Limits", secs+":error:project")
			fail(w, http.StatusTooManyRequests, "demasiados eventos")
			return
		}
	}

	respID := headerID
	for i, ev := range events {
		start := time.Now()
		res, err := h.ingester.Ingest(ctx, project.ID, ev)
		if err != nil {
			h.log.ErrorContext(ctx, "guardar evento", "proyecto", project.ID, "event_id", ev.ID, "err", err)
			fail(w, http.StatusInternalServerError, "error interno")
			return
		}
		if i == 0 {
			respID = ev.ID
		}
		h.log.DebugContext(ctx, "evento", "proyecto", project.ID, "event_id", ev.ID, "issue", res.IssueID,
			"resultado", res.Outcome.String(), "repetido", res.Duplicate, "duración", time.Since(start))
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": respID})
}

func fail(w http.ResponseWriter, code int, detail string) {
	writeJSON(w, code, map[string]string{"detail": detail})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
