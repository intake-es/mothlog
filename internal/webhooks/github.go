package webhooks

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/intake-es/intake-errors/internal/store"
)

type githubRepo struct {
	FullName string `json:"full_name"`
}

type githubUser struct {
	Login string `json:"login"`
}

type githubIssuesPayload struct {
	Action string `json:"action"`
	Issue  struct {
		Number      int    `json:"number"`
		HTMLURL     string `json:"html_url"`
		State       string `json:"state"`
		StateReason string `json:"state_reason"`
		// PullRequest está en las issues que son PRs.
		PullRequest json.RawMessage `json:"pull_request"`
	} `json:"issue"`
	Repository githubRepo `json:"repository"`
	Sender     githubUser `json:"sender"`
}

type githubPullPayload struct {
	Action      string `json:"action"`
	Number      int    `json:"number"`
	PullRequest struct {
		Number  int    `json:"number"`
		HTMLURL string `json:"html_url"`
		State   string `json:"state"`
		Title   string `json:"title"`
		Body    string `json:"body"`
		Merged  bool   `json:"merged"`
	} `json:"pull_request"`
	Repository githubRepo `json:"repository"`
	Sender     githubUser `json:"sender"`
}

func (h *Handler) github(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if h.opts.GitHubSecret == "" {
		reply(w, http.StatusServiceUnavailable, "falta GITHUB_WEBHOOK_SECRET en el servidor")
		return
	}
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	sig, found := strings.CutPrefix(r.Header.Get("X-Hub-Signature-256"), "sha256=")
	if !found {
		h.opts.Log.WarnContext(ctx, "webhook de GitHub rechazado", "motivo", "sin X-Hub-Signature-256")
		reply(w, http.StatusUnauthorized, errSignature.Error())
		return
	}
	if err := verify(h.opts.GitHubSecret, body, sig); err != nil {
		h.opts.Log.WarnContext(ctx, "webhook de GitHub rechazado", "motivo", err)
		reply(w, http.StatusUnauthorized, err.Error())
		return
	}
	event, delivery := r.Header.Get("X-GitHub-Event"), r.Header.Get("X-GitHub-Delivery")
	if delivery == "" {
		reply(w, http.StatusBadRequest, "falta X-GitHub-Delivery")
		return
	}
	// GitHub no fecha las entregas: una repetida se reconoce por su id.
	fresh, err := h.st.RecordWebhookDelivery(ctx, store.ProviderGitHub, delivery, event)
	if err != nil {
		h.opts.Log.ErrorContext(ctx, "webhook de GitHub", "err", err)
		reply(w, http.StatusInternalServerError, "error interno")
		return
	}
	if !fresh {
		h.opts.Log.WarnContext(ctx, "webhook de GitHub repetido", "entrega", delivery)
		reply(w, http.StatusOK, "ya recibida")
		return
	}
	switch event {
	case "issues":
		var p githubIssuesPayload
		if err := json.Unmarshal(body, &p); err != nil {
			reply(w, http.StatusBadRequest, "cuerpo no válido")
			return
		}
		err = h.githubIssue(r, p)
	case "pull_request":
		var p githubPullPayload
		if err := json.Unmarshal(body, &p); err != nil {
			reply(w, http.StatusBadRequest, "cuerpo no válido")
			return
		}
		err = h.githubPull(r, p)
	default:
		reply(w, http.StatusOK, "ignorada")
		return
	}
	if err != nil {
		h.failed(w, r, store.ProviderGitHub, delivery, err)
		return
	}
	reply(w, http.StatusOK, "ok")
}

func githubRef(repo string, number int) string { return repo + "#" + strconv.Itoa(number) }

// githubIssue lleva a los errores enlazados el cierre o la reapertura de la
// issue: cerrada como «no planeada» silencia; cerrada por otro motivo, resuelve.
func (h *Handler) githubIssue(r *http.Request, p githubIssuesPayload) error {
	ctx := r.Context()
	if (p.Action != "closed" && p.Action != "reopened") || p.Issue.Number == 0 || len(p.Issue.PullRequest) > 0 {
		return nil
	}
	links, err := h.st.LinksTo(ctx, store.ProviderGitHub, store.LinkIssue, githubRef(p.Repository.FullName, p.Issue.Number))
	if err != nil {
		return err
	}
	state, status := store.LinkOpen, "open"
	if p.Action == "closed" {
		state, status = store.LinkClosed, "resolved"
		if p.Issue.StateReason == "not_planned" {
			state, status = store.LinkCanceled, "silenced"
		}
	}
	by := githubActor(p.Sender)
	for _, l := range links {
		if err := h.st.SetLinkState(ctx, l.ID, state, p.Issue.State); err != nil {
			return err
		}
		by.LinkID = l.ID
		if err := h.apply(ctx, l.IssueID, status, by); err != nil {
			return err
		}
	}
	return nil
}

// githubPull enlaza el PR a los errores cuya URL lleva en el título o la
// descripción (sola o tras «Fixes»), y al mergearse los deja resueltos en la
// siguiente versión.
func (h *Handler) githubPull(r *http.Request, p githubPullPayload) error {
	ctx := r.Context()
	if !slices.Contains([]string{"opened", "edited", "reopened", "closed"}, p.Action) {
		return nil
	}
	pr := p.PullRequest
	if pr.Number == 0 {
		pr.Number = p.Number
	}
	ids := IssueIDsIn(h.base(r).String(), pr.Title+"\n"+pr.Body)
	state := store.LinkOpen
	switch {
	case pr.Merged:
		state = store.LinkMerged
	case pr.State == "closed":
		state = store.LinkClosed
	}
	ref := githubRef(p.Repository.FullName, pr.Number)
	by := githubActor(p.Sender)
	by.PullRequest = true
	for _, id := range ids {
		_, err := store.SaveLink(ctx, h.st.Pool(), store.IssueLink{IssueID: id, Provider: store.ProviderGitHub,
			Kind: store.LinkPullRequest, ExternalID: ref, Identifier: ref, URL: pr.HTMLURL, State: state, StateName: state}, false)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		if p.Action == "closed" && pr.Merged {
			if err := h.apply(ctx, id, "resolved_next", by); err != nil {
				return err
			}
		}
	}
	return nil
}

// IssueIDsIn saca de text los ids de los errores enlazados con su URL del
// panel, <base>/issues/<id>, por http o https y sin repetir.
func IssueIDsIn(base, text string) []int64 {
	base = strings.TrimRight(base, "/")
	_, rest, ok := strings.Cut(base, "://")
	if !ok || rest == "" {
		return nil
	}
	re := regexp.MustCompile(`(?i)https?://` + regexp.QuoteMeta(rest) + `/issues/(\d+)\b`)
	var out []int64
	for _, m := range re.FindAllStringSubmatch(text, -1) {
		id, err := strconv.ParseInt(m[1], 10, 64)
		if err == nil && id > 0 && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

func githubActor(u githubUser) store.ExternalChange {
	by := store.ExternalChange{Source: store.SourceGitHub, Actor: &store.Actor{Name: "GitHub", Kind: store.ActorUser}}
	if u.Login != "" {
		by.Actor.Name = u.Login
	}
	return by
}
