package store

import (
	"context"
	"errors"
	"slices"
	"testing"
)

func TestPeopleAndAssign(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	p, err := s.CreateProject(ctx, "web", randomHex(t, 16))
	if err != nil {
		t.Fatal(err)
	}
	newIssue := func(title string) int64 {
		var id int64
		if err := s.pool.QueryRow(ctx, `INSERT INTO issues (project_id, fingerprint, title, first_seen, last_seen, event_count)
			VALUES ($1, $2, $3, now(), now(), 1) RETURNING id`, p.ID, randomHex(t, 8), title).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	issue := newIssue("TypeError")

	avatar := "https://avatars.example.com/ana"
	if err := s.SavePerson(ctx, Person{Login: "ana", Name: "Ana Pérez", AvatarURL: &avatar}); err != nil {
		t.Fatal(err)
	}
	// Al volver a entrar se pone al día; sin nombre, el login.
	if err := s.SavePerson(ctx, Person{Login: "luis"}); err != nil {
		t.Fatal(err)
	}
	first, _ := s.People(ctx)
	if err := s.SavePerson(ctx, Person{Login: "luis", Name: "Luis"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SavePerson(ctx, Person{Login: " "}); !errors.Is(err, ErrInvalid) {
		t.Errorf("sin login: %v", err)
	}
	people, err := s.People(ctx)
	if err != nil || len(people) != 2 || people[0].Name != "Ana Pérez" || *people[0].AvatarURL != avatar ||
		people[1].Name != "Luis" || !people[1].LastLoginAt.After(first[1].LastLoginAt) {
		t.Fatalf("personas = %+v, %v", people, err)
	}

	if _, _, err := s.CreateToken(ctx, "agente"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CreateToken(ctx, "viejo"); err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeToken(ctx, "viejo"); err != nil {
		t.Fatal(err)
	}
	as, err := s.Assignees(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, a := range as {
		names = append(names, a.Kind+":"+a.Name)
	}
	if !slices.Equal(names, []string{"user:ana", "user:luis", "token:agente"}) || as[0].DisplayName != "Ana Pérez" {
		t.Errorf("asignables = %v %+v", names, as)
	}

	luis := Actor{Name: "luis", Kind: ActorUser}
	agente := Actor{Name: "agente", Kind: ActorToken}
	ui := AssignChange{Actor: &luis, Source: SourceUI}

	// A una persona, sin decir el tipo y sin distinguir mayúsculas.
	got, err := s.AssignIssue(ctx, issue, &Actor{Name: "ANA"}, ui)
	if err != nil || got.Assignee == nil || *got.Assignee != (Assignee{Name: "ana", Kind: ActorUser, DisplayName: "Ana Pérez", AvatarURL: got.Assignee.AvatarURL, Active: true}) ||
		*got.Assignee.AvatarURL != avatar {
		t.Fatalf("asignar a ana = %+v, %v", got.Assignee, err)
	}
	// Repetirlo no apunta nada.
	if _, err := s.AssignIssue(ctx, issue, &Actor{Name: "ana", Kind: ActorUser}, ui); err != nil {
		t.Fatal(err)
	}
	// A un token: se lo asigna el propio agente.
	got, err = s.AssignIssue(ctx, issue, &agente, AssignChange{Actor: &agente, Source: SourceMCP})
	if err != nil || got.Assignee == nil || got.Assignee.Kind != ActorToken || !got.Assignee.Active {
		t.Fatalf("asignar al agente = %+v, %v", got.Assignee, err)
	}
	if _, err := s.AssignIssue(ctx, issue, nil, ui); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Issue(ctx, issue); got.Assignee != nil {
		t.Errorf("desasignado = %+v", got.Assignee)
	}

	for _, bad := range []Actor{{Name: "nadie"}, {Name: "viejo"}, {Name: "ana", Kind: ActorToken}, {Name: "agente", Kind: ActorUser}, {Name: "ana", Kind: "robot"}} {
		if _, err := s.AssignIssue(ctx, issue, &bad, ui); !errors.Is(err, ErrInvalid) {
			t.Errorf("asignar a %+v: %v", bad, err)
		}
	}
	// Un nombre que es de una persona y de un token necesita el tipo.
	if _, _, err := s.CreateToken(ctx, "ana"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AssignIssue(ctx, issue, &Actor{Name: "ana"}, ui); !errors.Is(err, ErrInvalid) {
		t.Errorf("ana ambigua: %v", err)
	}
	if _, err := s.AssignIssue(ctx, issue+1000, &luis, ui); !errors.Is(err, ErrNotFound) {
		t.Errorf("issue que no existe: %v", err)
	}
	if _, err := s.AssignIssue(ctx, issue, &luis, AssignChange{Source: SourceIngest}); !errors.Is(err, ErrInvalid) {
		t.Errorf("origen ingest: %v", err)
	}

	// Quien se asigna a sí mismo con sesión queda guardado aunque no constara.
	marta := Actor{Name: "marta", Kind: ActorUser}
	if got, err := s.AssignIssue(ctx, issue, &marta, AssignChange{Actor: &marta, Source: SourceUI}); err != nil || got.Assignee.Name != "marta" {
		t.Fatalf("asignarse = %+v, %v", got.Assignee, err)
	}

	h, err := s.History(ctx, issue, nil)
	if err != nil {
		t.Fatal(err)
	}
	type step struct{ by, source, from, to string }
	name := func(a *Actor) string {
		if a == nil {
			return ""
		}
		return a.Kind + ":" + a.Name
	}
	var steps []step
	for _, e := range h {
		if e.Type != "assignment" {
			t.Fatalf("entrada %+v", e)
		}
		steps = append(steps, step{name(e.Author), e.Source, name(e.Previous), name(e.Assignee)})
	}
	want := []step{
		{"user:luis", "ui", "", "user:ana"},
		{"token:agente", "mcp", "user:ana", "token:agente"},
		{"user:luis", "ui", "token:agente", ""},
		{"user:marta", "ui", "", "user:marta"},
	}
	if !slices.Equal(steps, want) {
		t.Errorf("historial = %+v", steps)
	}

	// Revocar el token conserva la asignación, que se ve inactiva.
	other := newIssue("Timeout")
	if _, err := s.AssignIssue(ctx, other, &agente, ui); err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeToken(ctx, "agente"); err != nil {
		t.Fatal(err)
	}
	got, err = s.Issue(ctx, other)
	if err != nil || got.Assignee == nil || got.Assignee.Name != "agente" || got.Assignee.Active {
		t.Errorf("token revocado = %+v, %v", got.Assignee, err)
	}
	// Un token nuevo con el mismo nombre no es el de la asignación.
	if _, _, err := s.CreateToken(ctx, "agente"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Issue(ctx, other); got.Assignee.Active {
		t.Errorf("el token nuevo reactiva la asignación vieja")
	}
	// Los del token revocado no son del nuevo en assigned:me, pero sí en assigned:agente.
	for q, want := range map[string]int{"assigned:me": 0, "!assigned:me": 2, "assigned:agente": 1} {
		page, err := s.ListIssues(ctx, IssueFilter{Query: q, Viewer: &agente})
		if err != nil || len(page.Issues) != want {
			t.Errorf("%s del token nuevo = %d issues, %v; quiero %d", q, len(page.Issues), err, want)
		}
	}
	// Asignárselo el token nuevo no es «sin cambios»: pasa a él y queda en el historial.
	got, err = s.AssignIssue(ctx, other, &agente, AssignChange{Actor: &agente, Source: SourceMCP})
	if err != nil || got.Assignee == nil || !got.Assignee.Active {
		t.Fatalf("el token nuevo se lo asigna = %+v, %v", got.Assignee, err)
	}
	if page, _ := s.ListIssues(ctx, IssueFilter{Query: "assigned:me", Viewer: &agente}); len(page.Issues) != 1 || page.Issues[0].ID != other {
		t.Errorf("assigned:me tras asignárselo = %+v", page.Issues)
	}
	h, err = s.History(ctx, other, nil)
	if err != nil || len(h) != 2 || h[1].Author == nil || *h[1].Author != agente || h[1].Previous == nil || *h[1].Previous != agente {
		t.Errorf("historial de other = %+v, %v", h, err)
	}
	// Repetirlo con el mismo token sí es «sin cambios».
	if _, err := s.AssignIssue(ctx, other, &agente, AssignChange{Actor: &agente, Source: SourceMCP}); err != nil {
		t.Fatal(err)
	}
	if h, _ := s.History(ctx, other, nil); len(h) != 2 {
		t.Errorf("repetir apunta otra asignación: %d entradas", len(h))
	}
}

func TestSearchAssigned(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	p, err := s.CreateProject(ctx, "web", randomHex(t, 16))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SavePerson(ctx, Person{Login: "Ana"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CreateToken(ctx, "agente"); err != nil {
		t.Fatal(err)
	}
	ids := map[string]int64{}
	for _, title := range []string{"de-ana", "del-agente", "de-nadie", "resuelto-de-ana"} {
		var id int64
		if err := s.pool.QueryRow(ctx, `INSERT INTO issues (project_id, fingerprint, title, first_seen, last_seen, event_count)
			VALUES ($1, $2, $3, now(), now(), 1) RETURNING id`, p.ID, randomHex(t, 8), title).Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids[title] = id
	}
	by := AssignChange{Source: SourceAPI}
	for title, to := range map[string]Actor{"de-ana": {Name: "ana"}, "del-agente": {Name: "agente"}, "resuelto-de-ana": {Name: "ana"}} {
		if _, err := s.AssignIssue(ctx, ids[title], &to, by); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.SetIssueStatus(ctx, ids["resuelto-de-ana"], "resolved"); err != nil {
		t.Fatal(err)
	}

	ana := &Actor{Name: "Ana", Kind: ActorUser}
	agente := &Actor{Name: "agente", Kind: ActorToken}
	// Un token llamado como la persona no es ella.
	anaToken := &Actor{Name: "Ana", Kind: ActorToken}
	for _, c := range []struct {
		query  string
		viewer *Actor
		want   []string
	}{
		{"assigned:me", ana, []string{"de-ana"}},
		{"assigned:me", agente, []string{"del-agente"}},
		{"assigned:me", anaToken, nil},
		{"assigned:ME is:resolved", ana, []string{"resuelto-de-ana"}},
		{"!assigned:me", ana, []string{"de-nadie", "del-agente"}},
		{"!assigned:me", agente, []string{"de-ana", "de-nadie"}},
		{"assigned:ana", nil, []string{"de-ana"}},
		{"assigned:agente", nil, []string{"del-agente"}},
		{"!assigned:ana", nil, []string{"de-nadie", "del-agente"}},
		{"is:assigned", nil, []string{"de-ana", "del-agente"}},
		{"!is:assigned", nil, []string{"de-nadie"}},
		{"is:unassigned", nil, []string{"de-nadie"}},
		{"!is:unassigned", nil, []string{"de-ana", "del-agente"}},
		// is:assigned no mira el estado: sigue siendo open por defecto.
		{"is:assigned is:resolved", nil, []string{"resuelto-de-ana"}},
	} {
		page, err := s.ListIssues(ctx, IssueFilter{Query: c.query, Viewer: c.viewer})
		if err != nil {
			t.Errorf("%q: %v", c.query, err)
			continue
		}
		var got []string
		for _, i := range page.Issues {
			got = append(got, i.Title)
		}
		slices.Sort(got)
		if !slices.Equal(got, c.want) {
			t.Errorf("%q como %v = %v; quiero %v", c.query, c.viewer, got, c.want)
		}
	}
	if _, err := s.ListIssues(ctx, IssueFilter{Query: "assigned:me"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("assigned:me sin saber quién: %v", err)
	}
}

func TestPeopleFromSeveralProviders(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	p, err := s.CreateProject(ctx, "web", randomHex(t, 16))
	if err != nil {
		t.Fatal(err)
	}
	save := func(person Person) string {
		t.Helper()
		login, err := s.SaveIdentity(ctx, person)
		if err != nil {
			t.Fatal(err)
		}
		return login
	}
	if got := save(Person{Login: "ana", Name: "Ana de GitHub"}); got != "ana" {
		t.Fatalf("GitHub = %q", got)
	}
	// Otra persona, de OIDC, con el mismo nombre (sin mirar mayúsculas) no se mezcla.
	if got := save(Person{Login: "Ana", Name: "Ana de OIDC", Provider: LoginOIDC, Subject: "sub-1"}); got != "Ana~2" {
		t.Fatalf("OIDC con el login de otra = %q", got)
	}
	if got := save(Person{Login: "ana", Provider: LoginOIDC, Subject: "sub-2"}); got != "ana~3" {
		t.Fatalf("la tercera = %q", got)
	}
	// Al volver, se la reconoce por el sub aunque cambie el correo, y se pone al día.
	if got := save(Person{Login: "ana.nueva@example.com", Name: "Ana Nueva", Provider: LoginOIDC, Subject: "sub-1"}); got != "Ana~2" {
		t.Fatalf("de vuelta = %q", got)
	}
	if got := save(Person{Login: "ana", Name: "Ana de GitHub"}); got != "ana" {
		t.Fatalf("GitHub de vuelta = %q", got)
	}
	// El mismo sub en otro proveedor es otra persona.
	if got := save(Person{Login: "sub-1", Provider: LoginGitHub, Subject: "sub-1"}); got != "sub-1" {
		t.Fatalf("mismo sub en GitHub = %q", got)
	}

	people, err := s.People(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byLogin := map[string]Person{}
	for _, p := range people {
		byLogin[p.Login] = p
	}
	if len(people) != 4 || byLogin["ana"].Provider != LoginGitHub || byLogin["Ana~2"].Provider != LoginOIDC ||
		byLogin["Ana~2"].Name != "Ana Nueva" || byLogin["ana~3"].Provider != LoginOIDC {
		t.Fatalf("personas = %+v", people)
	}

	var issue int64
	if err := s.pool.QueryRow(ctx, `INSERT INTO issues (project_id, fingerprint, title, first_seen, last_seen, event_count)
		VALUES ($1, 'fp', 'TypeError', now(), now(), 1) RETURNING id`, p.ID).Scan(&issue); err != nil {
		t.Fatal(err)
	}
	oidcAna := Actor{Name: "Ana~2", Kind: ActorUser}
	got, err := s.AssignIssue(ctx, issue, &oidcAna, AssignChange{Actor: &oidcAna, Source: SourceUI})
	if err != nil || got.Assignee == nil || got.Assignee.Name != "Ana~2" || got.Assignee.DisplayName != "Ana Nueva" {
		t.Fatalf("asignar = %+v, %v", got.Assignee, err)
	}
	for viewer, want := range map[string]int{"Ana~2": 1, "ana": 0, "ana~3": 0} {
		page, err := s.ListIssues(ctx, IssueFilter{Query: "assigned:me", Viewer: &Actor{Name: viewer, Kind: ActorUser}})
		if err != nil || len(page.Issues) != want {
			t.Errorf("assigned:me como %s = %d, %v", viewer, len(page.Issues), err)
		}
	}
	h, err := s.History(ctx, issue, nil)
	if err != nil || len(h) != 1 || h[0].Author == nil || h[0].Author.Name != "Ana~2" {
		t.Errorf("historial = %+v, %v", h, err)
	}
}
