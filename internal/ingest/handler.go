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
	"github.com/intake-es/intake-errors/internal/metrics"
	"github.com/intake-es/intake-errors/internal/monitor"
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
	Filters(ctx context.Context, projectID int64) (store.Filters, error)
}

// CheckIns guarda los check-ins de los crons.
type CheckIns interface {
	CheckIn(ctx context.Context, projectID int64, in store.CheckInInput) (store.CheckInResult, error)
}

type Handler struct {
	projects Projects
	ingester *Ingester
	limiter  *RateLimiter
	drops    *Drops
	checkIns CheckIns
	metrics  *metrics.Metrics
	log      *slog.Logger
}

// WithMetrics hace que el handler cuente en m lo que recibe; sin él, no cuenta.
func (h *Handler) WithMetrics(m *metrics.Metrics) *Handler {
	h.metrics = m
	return h
}

func (h *Handler) drop(projectID int64, reason string, n int) {
	h.drops.Add(projectID, reason, n)
	h.metrics.Dropped(projectID, reason, n)
}

// WithCheckIns hace que el handler guarde los items check_in; sin él, se ignoran.
func (h *Handler) WithCheckIns(c CheckIns) *Handler {
	h.checkIns = c
	return h
}

// NewHandler acepta drops nil: entonces no se cuentan los descartes.
func NewHandler(projects Projects, ingester *Ingester, limiter *RateLimiter, drops *Drops, log *slog.Logger) *Handler {
	return &Handler{projects: projects, ingester: ingester, limiter: limiter, drops: drops, log: log}
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
		if h.metrics == nil {
			h.envelope(w, r)
			return
		}
		start := time.Now()
		cw, code := metrics.CodeWriter(w)
		h.envelope(cw, r)
		h.metrics.Envelope(code(), time.Since(start))
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
	var checkIns []store.CheckInInput
	if h.checkIns != nil {
		for _, it := range env.CheckIns() {
			in, err := monitor.Parse(it.Payload)
			if err != nil {
				// Un check-in roto no tumba los eventos que viajan con él.
				h.log.WarnContext(ctx, "check-in ignorado", "proyecto", project.ID, "err", err)
				continue
			}
			checkIns = append(checkIns, in)
		}
	}

	h.metrics.Received(project.ID, len(events))
	lim := DefaultLimits()
	perMinute := 0
	sessions := env.Sessions()
	if len(events) > 0 || len(checkIns) > 0 || len(sessions) > 0 {
		filters, err := h.projects.Filters(ctx, project.ID)
		if err != nil {
			h.log.ErrorContext(ctx, "leer filtros", "proyecto", project.ID, "err", err)
			fail(w, http.StatusInternalServerError, "error interno")
			return
		}
		lim = LimitsOf(filters)
		if filters.RateLimitPerMinute != nil {
			perMinute = *filters.RateLimitPerMinute
		}
		fl := NewFilter(filters)
		// Las sesiones no gastan del límite de eventos.
		h.sessions(ctx, project.ID, sessions, fl)
		kept := events[:0]
		for _, ev := range events {
			if reason := fl.Drop(ev); reason != "" {
				h.drop(project.ID, reason, 1)
				h.log.DebugContext(ctx, "evento descartado", "proyecto", project.ID, "event_id", ev.ID, "motivo", reason)
				continue
			}
			kept = append(kept, ev)
		}
		events = kept
	}

	// Los check-ins cuentan en el mismo límite por minuto que los eventos.
	if n := len(events) + len(checkIns); n > 0 {
		if ok, wait := h.limiter.AllowUpTo(project.ID, n, perMinute); !ok {
			if len(events) > 0 {
				h.drop(project.ID, store.DropRateLimit, len(events))
			}
			var cats []string
			if len(events) > 0 {
				cats = append(cats, "error")
			}
			if len(checkIns) > 0 {
				cats = append(cats, "monitor")
			}
			secs := strconv.Itoa(max(1, int(math.Ceil(wait.Seconds()))))
			hdr := w.Header()
			hdr.Set("Retry-After", secs)
			hdr.Set("X-Sentry-Rate-Limits", secs+":"+strings.Join(cats, ";")+":project")
			fail(w, http.StatusTooManyRequests, "demasiados eventos")
			return
		}
	}

	respID := headerID
	for i, ev := range events {
		start := time.Now()
		res, err := h.ingester.IngestWith(ctx, project.ID, ev, lim)
		if err != nil {
			h.log.ErrorContext(ctx, "guardar evento", "proyecto", project.ID, "event_id", ev.ID, "err", err)
			fail(w, http.StatusInternalServerError, "error interno")
			return
		}
		if i == 0 {
			respID = ev.ID
		}
		switch {
		case res.Duplicate:
			h.metrics.Dropped(project.ID, metrics.DropDuplicate, 1)
		case res.Dropped != "":
			h.drop(project.ID, res.Dropped, 1)
		default:
			h.metrics.Saved(project.ID)
		}
		h.log.DebugContext(ctx, "evento", "proyecto", project.ID, "event_id", ev.ID, "issue", res.IssueID,
			"resultado", res.Outcome.String(), "repetido", res.Duplicate, "descartado", res.Dropped, "duración", time.Since(start))
	}
	for _, in := range checkIns {
		res, err := h.checkIns.CheckIn(ctx, project.ID, in)
		if err != nil {
			h.log.ErrorContext(ctx, "guardar check-in", "proyecto", project.ID, "monitor", in.Slug, "err", err)
			fail(w, http.StatusInternalServerError, "error interno")
			return
		}
		if res.Outcome == store.CheckInIgnored {
			h.metrics.CheckIn("ignored")
		} else {
			h.metrics.CheckIn(in.Status)
		}
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
