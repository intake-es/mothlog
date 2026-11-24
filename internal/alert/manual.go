package alert

import (
	"context"
	"errors"

	"github.com/intake-es/mothlog/internal/store"
)

// ErrLinearOff es pedir una issue de Linear a un servidor sin LINEAR_API_KEY.
var ErrLinearOff = errors.New("the server has no Linear configured")

// LinearError es un fallo de Linear al crear la issue a mano. Lleva el
// detalle para el registro; a quien la pide se le dice solo que ha fallado.
type LinearError struct{ Err error }

func (e *LinearError) Error() string { return e.Err.Error() }
func (e *LinearError) Unwrap() error { return e.Err }

// CreateLinear crea a mano la issue de Linear del error, igual que el aviso
// por defecto (equipo, etiquetas y Markdown), y la deja enlazada con by como
// autor para que se sincronicen los estados. Si el error ya tiene una, la
// devuelve sin crear otra y created es false. Usa el mismo id de creación que
// los avisos, así que no duplica la que esté creando uno a la vez.
func (a *Alerter) CreateLinear(ctx context.Context, issueID int64, by store.Actor) (link store.IssueLink, created bool, err error) {
	l, ok := a.channels[channelLinear].(*linear)
	if !ok {
		return link, false, ErrLinearOff
	}
	if link, ok, err := a.linearLink(ctx, issueID); err != nil || ok {
		return link, false, err
	}
	createID, err := a.st.ClaimLinearCreateID(ctx, issueID, newUUID())
	if err != nil {
		return link, false, err
	}
	p := &pending{l: a.lang, issueID: issueID, kind: "new", createID: createID, eventAt: a.now().UTC()}
	if err := a.loadIssue(ctx, a.st.Pool(), p); err != nil {
		return link, false, err
	}
	is, err := l.create(ctx, p)
	if err != nil {
		return link, false, &LinearError{Err: err}
	}
	link, err = store.SaveLink(ctx, a.st.Pool(), store.IssueLink{IssueID: issueID, Provider: store.ProviderLinear,
		Kind: store.LinkIssue, ExternalID: is.ID, Identifier: is.Identifier, URL: is.URL, State: store.LinkOpen, Author: &by}, true)
	if err != nil {
		return link, false, err
	}
	a.log.InfoContext(ctx, "Linear issue created by hand", "issue", issueID, "linear", is.Identifier, "by", by.Name)
	return link, true, nil
}

// linearLink es la issue de Linear del error, si tiene.
func (a *Alerter) linearLink(ctx context.Context, issueID int64) (store.IssueLink, bool, error) {
	links, err := a.st.IssueLinks(ctx, issueID)
	if err != nil {
		return store.IssueLink{}, false, err
	}
	for i := len(links) - 1; i >= 0; i-- {
		if links[i].Provider == store.ProviderLinear && links[i].Kind == store.LinkIssue {
			return links[i], true, nil
		}
	}
	return store.IssueLink{}, false, nil
}
