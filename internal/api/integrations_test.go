package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/intake-es/mothlog/internal/api"
	"github.com/intake-es/mothlog/internal/store"
	"github.com/intake-es/mothlog/internal/store/storetest"
	"github.com/intake-es/mothlog/internal/webhooks"
)

func TestIssueDetailLinks(t *testing.T) {
	e := setup(t)
	p := e.db.AddProject(t, "backend")
	now := time.Now().UTC()
	id := e.db.AddIssue(t, storetest.Issue{ProjectID: p.ID, Title: "KeyError", FirstSeen: now, LastSeen: now, Count: 1})
	var d store.IssueDetail
	e.get(t, "/api/0/issues/"+itoa(id)+"/", &d)
	if d.Links == nil || len(d.Links) != 0 {
		t.Fatalf("sin enlaces quiero [], no %#v", d.Links)
	}
	ctx := context.Background()
	for _, l := range []store.IssueLink{
		{IssueID: id, Provider: store.ProviderLinear, Kind: store.LinkIssue, ExternalID: "lin-1", Identifier: "ERR-1",
			URL: "https://linear.app/x/issue/ERR-1", State: store.LinkClosed, StateName: "Done"},
		{IssueID: id, Provider: store.ProviderGitHub, Kind: store.LinkPullRequest, ExternalID: "o/r#4", Identifier: "o/r#4",
			URL: "https://github.com/o/r/pull/4", State: store.LinkMerged},
	} {
		if _, err := store.SaveLink(ctx, e.db.Pool(), l, false); err != nil {
			t.Fatal(err)
		}
	}
	e.get(t, "/api/0/issues/"+itoa(id)+"/", &d)
	if len(d.Links) != 2 || d.Links[0].Identifier != "ERR-1" || d.Links[0].State != "closed" || d.Links[0].StateName != "Done" ||
		d.Links[1].Kind != "pull_request" || d.Links[1].State != "merged" {
		t.Fatalf("enlaces = %+v", d.Links)
	}
	code, md := e.do(t, http.MethodGet, "/api/0/issues/"+itoa(id)+"/context.md", "")
	if code != http.StatusOK || !strings.Contains(string(md), "- Linear issue ERR-1: closed (Done), https://linear.app/x/issue/ERR-1") {
		t.Errorf("context.md (%d) sin los enlaces:\n%s", code, md)
	}
}

func TestIntegrations(t *testing.T) {
	db := storetest.NewDB(t)
	hooks := webhooks.New(db.Store, webhooks.Options{LinearSecret: "s", LinearAPIKey: true})
	mux := http.NewServeMux()
	if err := api.Register(mux, db.Store, api.Options{PublicURL: "https://errors.example.com/", Integrations: hooks}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(mux)
	defer srv.Close()
	_, token, err := db.CreateToken(context.Background(), "tests")
	if err != nil {
		t.Fatal(err)
	}
	e := &env{db: db, srv: srv, token: token}
	if _, err := db.RecordWebhookDelivery(context.Background(), store.ProviderLinear, "d1", []byte(`{}`), "Issue.update"); err != nil {
		t.Fatal(err)
	}
	var s webhooks.Status
	e.get(t, "/api/0/integrations/", &s)
	if s.Linear.WebhookURL != "https://errors.example.com/webhooks/linear" || !s.Linear.SecretSet || !s.Linear.TokenSet || s.Linear.LastDelivery == nil {
		t.Errorf("linear = %+v", s.Linear)
	}
	if s.GitHub.WebhookURL != "https://errors.example.com/webhooks/github" || s.GitHub.SecretSet || s.GitHub.TokenSet || s.GitHub.LastDelivery != nil {
		t.Errorf("github = %+v", s.GitHub)
	}
	if code, _ := (&env{db: db, srv: srv}).do(t, http.MethodGet, "/api/0/integrations/", ""); code != http.StatusUnauthorized {
		t.Errorf("sin token: %d", code)
	}
}
