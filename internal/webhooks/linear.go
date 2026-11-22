package webhooks

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/intake-es/intake-errors/internal/store"
)

// linearPayload es lo que se usa del cuerpo de un webhook de Linear.
type linearPayload struct {
	Action string `json:"action"`
	Type   string `json:"type"`
	Data   struct {
		ID         string `json:"id"`
		Identifier string `json:"identifier"`
		URL        string `json:"url"`
		State      *struct {
			Name string `json:"name"`
			Type string `json:"type"`
		} `json:"state"`
	} `json:"data"`
	// UpdatedFrom trae los valores de antes de lo que ha cambiado.
	UpdatedFrom map[string]json.RawMessage `json:"updatedFrom"`
	Actor       *struct {
		Name  string `json:"name"`
		Email string `json:"email"`
		Type  string `json:"type"`
	} `json:"actor"`
	WebhookTimestamp int64  `json:"webhookTimestamp"`
	WebhookID        string `json:"webhookId"`
}

func (h *Handler) linear(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if h.opts.LinearSecret == "" {
		reply(w, http.StatusServiceUnavailable, "falta LINEAR_WEBHOOK_SECRET en el servidor")
		return
	}
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	if err := verify(h.opts.LinearSecret, body, r.Header.Get("Linear-Signature")); err != nil {
		h.opts.Log.WarnContext(ctx, "webhook de Linear rechazado", "motivo", err)
		reply(w, http.StatusUnauthorized, err.Error())
		return
	}
	var p linearPayload
	if err := json.Unmarshal(body, &p); err != nil {
		reply(w, http.StatusBadRequest, "cuerpo no válido")
		return
	}
	sent := time.UnixMilli(p.WebhookTimestamp)
	if age := h.opts.Now().Sub(sent); p.WebhookTimestamp == 0 || age > MaxAge || age < -MaxAge {
		h.opts.Log.WarnContext(ctx, "webhook de Linear rechazado", "motivo", "entrega vieja", "enviado", sent)
		reply(w, http.StatusUnauthorized, "entrega vieja o sin webhookTimestamp")
		return
	}
	delivery := r.Header.Get("Linear-Delivery")
	if delivery == "" {
		delivery = p.WebhookID + ":" + strconv.FormatInt(p.WebhookTimestamp, 10) + ":" + p.Data.ID
	}
	fresh, err := h.st.RecordWebhookDelivery(ctx, store.ProviderLinear, delivery, p.Type+"."+p.Action)
	if err != nil {
		h.opts.Log.ErrorContext(ctx, "webhook de Linear", "err", err)
		reply(w, http.StatusInternalServerError, "error interno")
		return
	}
	if !fresh {
		reply(w, http.StatusOK, "ya recibida")
		return
	}
	if p.Type != "Issue" || p.Action != "update" || p.Data.ID == "" || p.Data.State == nil {
		reply(w, http.StatusOK, "ignorada")
		return
	}
	if _, changed := p.UpdatedFrom["stateId"]; !changed {
		reply(w, http.StatusOK, "sin cambio de estado")
		return
	}
	links, err := h.st.LinksTo(ctx, store.ProviderLinear, store.LinkIssue, p.Data.ID)
	if err != nil {
		h.failed(w, r, store.ProviderLinear, delivery, err)
		return
	}
	category := store.LinearStateCategory(p.Data.State.Type)
	// Pasar de un estado abierto a otro (de «Todo» a «In Progress») no reabre:
	// solo salir de uno completado o cancelado.
	wasClosed := false
	for _, k := range []string{"completedAt", "canceledAt"} {
		if v, ok := p.UpdatedFrom[k]; ok && string(v) != "null" {
			wasClosed = true
		}
	}
	// Sin actor (una automatización de Linear), el cambio va a nombre de Linear.
	by := store.ExternalChange{Source: store.SourceLinear, Actor: &store.Actor{Name: "Linear", Kind: store.ActorUser}}
	if p.Actor != nil && p.Actor.Name != "" {
		by.Actor.Name = p.Actor.Name
	}
	for _, l := range links {
		if err := h.st.SetLinkState(ctx, l.ID, category, p.Data.State.Name); err != nil {
			h.failed(w, r, store.ProviderLinear, delivery, err)
			return
		}
		status := ""
		switch {
		case category == store.LinkClosed:
			status = "resolved"
		case category == store.LinkCanceled:
			status = "silenced"
		case wasClosed || l.State == store.LinkClosed || l.State == store.LinkCanceled:
			status = "open"
		}
		if status == "" {
			continue
		}
		by.LinkID = l.ID
		if err := h.apply(ctx, l.IssueID, status, by); err != nil {
			h.failed(w, r, store.ProviderLinear, delivery, err)
			return
		}
	}
	reply(w, http.StatusOK, "ok")
}
