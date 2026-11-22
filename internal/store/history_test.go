package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestComments(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	p, err := s.CreateProject(ctx, "web", randomHex(t, 16))
	if err != nil {
		t.Fatal(err)
	}
	var issue int64
	if err := s.pool.QueryRow(ctx, `INSERT INTO issues (project_id, fingerprint, title, first_seen, last_seen, event_count)
		VALUES ($1, 'fp', 'TypeError', now(), now(), 1) RETURNING id`, p.ID).Scan(&issue); err != nil {
		t.Fatal(err)
	}
	ana := Actor{Name: "ana", Kind: ActorUser}
	agente := Actor{Name: "agente", Kind: ActorToken}
	// Una persona y un token con el mismo nombre no son el mismo autor.
	anaToken := Actor{Name: "ana", Kind: ActorToken}

	c, err := s.AddComment(ctx, issue, ana, "  La causa es **el carrito vacío**.\n")
	if err != nil {
		t.Fatal(err)
	}
	if c.Body != "La causa es **el carrito vacío**." || c.Author != ana || !c.Mine {
		t.Errorf("comentario = %+v", c)
	}
	time.Sleep(time.Millisecond)
	if _, err := s.AddComment(ctx, issue, agente, "Arreglado en el commit abc123"); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", "   ", strings.Repeat("a", MaxCommentLen+1)} {
		if _, err := s.AddComment(ctx, issue, ana, bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("comentario %.10q: %v", bad, err)
		}
	}
	if _, err := s.AddComment(ctx, issue, Actor{Kind: ActorUser}, "sin nombre"); !errors.Is(err, ErrInvalid) {
		t.Errorf("sin autor: %v", err)
	}
	if _, err := s.AddComment(ctx, issue+1000, ana, "hola"); !errors.Is(err, ErrNotFound) {
		t.Errorf("issue que no existe: %v", err)
	}

	h, err := s.History(ctx, issue, &agente)
	if err != nil {
		t.Fatal(err)
	}
	if len(h) != 2 || h[0].Body != c.Body || h[0].Mine || h[1].Author.Name != "agente" || !h[1].Mine {
		t.Fatalf("historial = %+v", h)
	}

	for _, who := range []Actor{agente, anaToken} {
		if err := s.DeleteComment(ctx, issue, c.ID, who); !errors.Is(err, ErrNotOwner) {
			t.Errorf("%+v borra el de ana: %v", who, err)
		}
	}
	if err := s.DeleteComment(ctx, issue+1000, c.ID, ana); !errors.Is(err, ErrNotFound) {
		t.Errorf("con otro issue: %v", err)
	}
	if err := s.DeleteComment(ctx, issue, c.ID, ana); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteComment(ctx, issue, c.ID, ana); !errors.Is(err, ErrNotFound) {
		t.Errorf("borrarlo dos veces: %v", err)
	}
	if h, _ := s.History(ctx, issue, nil); len(h) != 1 || h[0].Mine {
		t.Errorf("tras borrar = %+v", h)
	}
	if _, err := s.History(ctx, issue+1000, nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("historial de un issue que no existe: %v", err)
	}
}

func TestStatusChangeComment(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	p, err := s.CreateProject(ctx, "web", randomHex(t, 16))
	if err != nil {
		t.Fatal(err)
	}
	var issue int64
	if err := s.pool.QueryRow(ctx, `INSERT INTO issues (project_id, fingerprint, title, first_seen, last_seen, event_count)
		VALUES ($1, 'fp', 'TypeError', now(), now(), 1) RETURNING id`, p.ID).Scan(&issue); err != nil {
		t.Fatal(err)
	}
	agente := &Actor{Name: "agente", Kind: ActorToken}
	if _, err := s.ChangeIssueStatus(ctx, issue, "resolved", StatusChange{Actor: agente, Source: SourceMCP, Comment: "Arreglado"}); err != nil {
		t.Fatal(err)
	}
	// Sin cambio, el comentario queda suelto; y sin eventos con release, resolved_next no apunta nada.
	if _, err := s.ChangeIssueStatus(ctx, issue, "resolved", StatusChange{Actor: agente, Source: SourceMCP, Comment: "Sigue arreglado"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ChangeIssueStatus(ctx, issue, "resolved_next", StatusChange{Actor: agente, Source: SourceMCP, Comment: "no"}); !errors.Is(err, ErrNoRelease) {
		t.Errorf("resolved_next sin release: %v", err)
	}
	if _, err := s.ChangeIssueStatus(ctx, issue, "open", StatusChange{Source: SourceMCP, Comment: "sin autor"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("comentario sin autor: %v", err)
	}
	if _, err := s.ChangeIssueStatus(ctx, issue, "open", StatusChange{Actor: agente, Source: "cli"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("origen no válido: %v", err)
	}
	if _, err := s.ChangeIssueStatus(ctx, issue+1000, "open", StatusChange{Actor: agente, Source: SourceAPI}); !errors.Is(err, ErrNotFound) {
		t.Errorf("issue que no existe: %v", err)
	}
	h, err := s.History(ctx, issue, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(h) != 2 || h[0].Type != "status" || h[0].Comment == nil || h[0].Comment.Body != "Arreglado" ||
		h[1].Type != "comment" || h[1].Body != "Sigue arreglado" {
		t.Fatalf("historial = %+v", h)
	}
	is, err := s.Issue(ctx, issue)
	if err != nil || is.Status != "resolved" {
		t.Errorf("estado = %+v, %v", is, err)
	}
}
