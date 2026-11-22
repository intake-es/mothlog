package api

import (
	"context"
	"net/http"

	"github.com/intake-es/intake-errors/internal/webhooks"
)

// Integrations dice cómo están Linear y GitHub: las URL de sus webhooks y si
// el servidor tiene sus secretos y tokens.
type Integrations interface {
	Status(ctx context.Context, base string) (webhooks.Status, error)
}

func (h *handlers) integrations(w http.ResponseWriter, r *http.Request) {
	if h.integ == nil {
		writeError(w, http.StatusNotFound, "no encontrado")
		return
	}
	s, err := h.integ.Status(r.Context(), h.baseURL(r).String())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, s)
}
