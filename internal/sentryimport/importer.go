package sentryimport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"slices"
	"strconv"
	"time"

	"github.com/intake-es/mothlog/internal/event"
	"github.com/intake-es/mothlog/internal/ingest"
	"github.com/intake-es/mothlog/internal/store"
)

// DefaultEvents es cuántos eventos se traen de cada issue si no se dice.
const DefaultEvents = 5

type Options struct {
	Org string
	// Projects son los slugs que se importan; vacío, todos.
	Projects []string
	// Since deja fuera los issues vistos por última vez y las releases
	// creadas antes de esa fecha.
	Since time.Time
	// Events es cuántos de los últimos eventos se traen de cada issue.
	Events int
	DryRun bool
	// Progress recibe una línea por cada paso; nil, ninguna.
	Progress io.Writer
}

type Importer struct {
	client *Client
	db     *store.Store
	ing    *ingest.Ingester
	opts   Options
	origin store.Origin
	report *Report
}

func New(client *Client, db *store.Store, ing *ingest.Ingester, opts Options) *Importer {
	if opts.Events <= 0 {
		opts.Events = DefaultEvents
	}
	if opts.Progress == nil {
		opts.Progress = io.Discard
	}
	return &Importer{
		client: client, db: db, ing: ing, opts: opts,
		origin: store.Origin{Source: "sentry", Server: client.Server(), Org: opts.Org},
	}
}

// Run importa todo y devuelve el informe. Solo devuelve error si no se puede
// seguir (no se pueden listar los proyectos o se cancela ctx); lo que falla
// con un proyecto, un issue o un evento queda en el informe y se sigue.
func (im *Importer) Run(ctx context.Context) (*Report, error) {
	im.report = &Report{Server: im.origin.Server, Org: im.opts.Org, DryRun: im.opts.DryRun}
	prev := im.client.Waited
	im.client.Waited = func(d time.Duration, why string) {
		im.progress("  %s: waiting %s", why, d.Round(time.Millisecond))
		if prev != nil {
			prev(d, why)
		}
	}
	defer func() { im.client.Waited = prev }()

	var projects []apiProject
	err := im.client.List(ctx, path("organizations", im.opts.Org, "projects"), nil, func(raw json.RawMessage) (bool, error) {
		var p apiProject
		if err := json.Unmarshal(raw, &p); err != nil || p.ID == "" || p.Slug == "" {
			im.report.errorf("unreadable project in the list")
			return true, nil
		}
		if len(im.opts.Projects) == 0 || slices.Contains(im.opts.Projects, string(p.Slug)) {
			projects = append(projects, p)
		}
		return true, nil
	})
	if err != nil {
		return im.report, fmt.Errorf("list projects of %s: %w", im.opts.Org, err)
	}
	for _, slug := range im.opts.Projects {
		if !slices.ContainsFunc(projects, func(p apiProject) bool { return string(p.Slug) == slug }) {
			im.report.errorf("there is no project %q in organization %s", slug, im.opts.Org)
		}
	}
	im.progress("%d projects in %s", len(projects), im.opts.Org)
	for _, p := range projects {
		if err := im.project(ctx, p); err != nil {
			if ctx.Err() != nil {
				return im.report, ctx.Err()
			}
			im.report.errorf("project %s: %v", p.Slug, err)
		}
	}
	return im.report, nil
}

func (im *Importer) progress(format string, args ...any) {
	_, _ = fmt.Fprintf(im.opts.Progress, format+"\n", args...)
}

func (im *Importer) project(ctx context.Context, p apiProject) error {
	name := firstOf(string(p.Name), string(p.Slug))
	id, act, err := im.db.ImportProject(ctx, im.origin, string(p.ID), name, im.opts.DryRun)
	if err != nil {
		im.report.Projects.Skipped++
		return err
	}
	im.report.Projects.add(act)
	im.progress("project %s: %s", p.Slug, actionWord(act, im.opts.DryRun))

	if err := im.releases(ctx, p, id); err != nil {
		if ctx.Err() != nil {
			return err
		}
		im.report.errorf("releases of %s: %v", p.Slug, err)
	}
	return im.issues(ctx, p, id)
}

func (im *Importer) releases(ctx context.Context, p apiProject, projectID int64) error {
	q := url.Values{"project": {string(p.ID)}, "per_page": {strconv.Itoa(pageSize)}}
	n := 0
	err := im.client.List(ctx, path("organizations", im.opts.Org, "releases"), q, func(raw json.RawMessage) (bool, error) {
		var r apiRelease
		if json.Unmarshal(raw, &r) != nil || r.Version == "" {
			im.report.Releases.Skipped++
			im.report.errorf("unreadable release in %s", p.Slug)
			return true, nil
		}
		if !im.opts.Since.IsZero() && !r.DateCreated.IsZero() && r.DateCreated.Before(im.opts.Since) {
			im.report.Releases.Skipped++
			return true, nil
		}
		created := r.DateCreated.Time
		if created.IsZero() {
			created = time.Now().UTC()
		}
		rel := store.ImportedRelease{ExternalID: string(p.ID) + ":" + string(r.Version), Version: string(r.Version), Created: created}
		if !r.DateReleased.IsZero() {
			rel.Released = &r.DateReleased.Time
		}
		relID, act, err := im.db.ImportRelease(ctx, im.origin, projectID, rel, im.opts.DryRun)
		if err != nil {
			im.report.Releases.Skipped++
			im.report.errorf("release %s of %s: %v", r.Version, p.Slug, err)
			return true, nil
		}
		im.report.Releases.add(act)
		n++
		if err := im.deploys(ctx, p, string(r.Version), relID); err != nil {
			if ctx.Err() != nil {
				return false, err
			}
			im.report.errorf("deploys of %s in %s: %v", r.Version, p.Slug, err)
		}
		return true, nil
	})
	im.progress("  releases: %d", n)
	return err
}

func (im *Importer) deploys(ctx context.Context, p apiProject, version string, releaseID int64) error {
	return im.client.List(ctx, path("organizations", im.opts.Org, "releases", version, "deploys"), nil,
		func(raw json.RawMessage) (bool, error) {
			var d apiDeploy
			if json.Unmarshal(raw, &d) != nil || d.ID == "" || d.Environment == "" {
				im.report.Deploys.Skipped++
				return true, nil
			}
			at := d.DateFinished.Time
			if at.IsZero() {
				at = d.DateStarted.Time
			}
			if at.IsZero() {
				im.report.Deploys.Skipped++
				return true, nil
			}
			act, err := im.db.ImportDeploy(ctx, im.origin, releaseID, string(p.ID)+":"+string(d.ID), string(d.Environment), at, im.opts.DryRun)
			if err != nil {
				im.report.Deploys.Skipped++
				im.report.errorf("deploy %s of %s: %v", d.ID, version, err)
				return true, nil
			}
			im.report.Deploys.add(act)
			return true, nil
		})
}

func (im *Importer) issues(ctx context.Context, p apiProject, projectID int64) error {
	q := url.Values{"query": {""}, "statsPeriod": {""}, "per_page": {strconv.Itoa(pageSize)}}
	if !im.opts.Since.IsZero() {
		q.Set("query", "lastSeen:>="+im.opts.Since.UTC().Format(time.RFC3339))
	}
	n := 0
	err := im.client.List(ctx, path("projects", im.opts.Org, string(p.Slug), "issues"), q, func(raw json.RawMessage) (bool, error) {
		var is apiIssue
		if json.Unmarshal(raw, &is) != nil || is.ID == "" {
			im.report.Issues.Skipped++
			im.report.errorf("unreadable issue in %s", p.Slug)
			return true, nil
		}
		if !im.opts.Since.IsZero() && !is.LastSeen.IsZero() && is.LastSeen.Before(im.opts.Since) {
			im.report.Issues.Skipped++
			return true, nil
		}
		if err := im.issue(ctx, p, projectID, is); err != nil {
			if ctx.Err() != nil {
				return false, err
			}
			im.report.Issues.Skipped++
			im.report.errorf("issue %s: %v", issueName(is), err)
		}
		if n++; n%50 == 0 {
			im.progress("  issues: %d…", n)
		}
		return true, nil
	})
	im.progress("  issues: %d", n)
	return err
}

func issueName(is apiIssue) string {
	if is.ShortID != "" {
		return fmt.Sprintf("%s (id %s)", is.ShortID, is.ID)
	}
	return "id " + string(is.ID)
}

func (im *Importer) issue(ctx context.Context, p apiProject, projectID int64, is apiIssue) error {
	evs, err := im.events(ctx, is)
	if err != nil {
		return err
	}
	if len(evs) == 0 {
		im.report.Issues.Skipped++
		im.report.skip("issue %s: none of its events are left in Sentry, and retention would delete it without events", issueName(is))
		return nil
	}
	newest := evs[0]
	status, fixedIn, resolvedIn := mapStatus(is, newest.Release)
	in := store.ImportedIssue{
		ExternalID:  string(is.ID),
		Fingerprint: ingest.Group(newest).Fingerprint,
		Title:       string(is.Title),
		Culprit:     string(is.Culprit),
		Type:        string(is.Metadata.Type),
		Level:       string(is.Level),
		Status:      status,
		FixedIn:     fixedIn,
		ResolvedIn:  resolvedIn,
		Count:       int64(is.Count),
		UserCount:   int64(is.UserCount),
		FirstSeen:   is.FirstSeen.Time,
		LastSeen:    is.LastSeen.Time,
	}
	for _, ev := range evs {
		if in.FirstSeen.IsZero() || ev.Timestamp.Before(in.FirstSeen) {
			in.FirstSeen = ev.Timestamp
		}
		if ev.Timestamp.After(in.LastSeen) {
			in.LastSeen = ev.Timestamp
		}
	}
	in.Count = max(in.Count, int64(len(evs)))
	in.FirstRelease = releaseOf(is.FirstRelease, "")
	if in.FirstRelease == "" {
		in.FirstRelease = evs[len(evs)-1].Release
	}
	res, err := im.db.ImportIssue(ctx, im.origin, projectID, in, im.opts.DryRun)
	if err != nil {
		return err
	}
	im.report.Issues.add(res.Action)
	if !res.Grouped {
		im.report.NotGrouped = append(im.report.NotGrouped, fmt.Sprintf("%s in %s: %s", issueName(is), p.Slug, res.NotGrouped))
	}
	created := 0
	for i := len(evs) - 1; i >= 0; i-- {
		ev := evs[i]
		var ok bool
		var err error
		if im.opts.DryRun {
			ok = res.ID == 0
			if !ok {
				var has bool
				has, err = im.db.HasEvent(ctx, projectID, ev.ID)
				ok = !has
			}
		} else {
			ok, err = im.ing.ImportEvent(ctx, projectID, res.ID, ev)
		}
		if err != nil {
			im.report.Events.Skipped++
			im.report.errorf("event %s of %s: %v", ev.ID, issueName(is), err)
			continue
		}
		if ok {
			created++
			im.report.Events.Created++
		} else {
			im.report.Events.Unchanged++
		}
	}
	if created > 0 && !im.opts.DryRun {
		return im.db.SettleImportedUsers(ctx, res.ID, in.UserCount)
	}
	return nil
}

// events trae los últimos eventos del issue, del más nuevo al más viejo,
// leídos con event.Parse como en la ingesta.
func (im *Importer) events(ctx context.Context, is apiIssue) ([]*event.Event, error) {
	var out []*event.Event
	q := url.Values{"full": {"true"}, "per_page": {strconv.Itoa(min(im.opts.Events, pageSize))}}
	err := im.client.List(ctx, path("organizations", im.opts.Org, "issues", string(is.ID), "events"), q,
		func(raw json.RawMessage) (bool, error) {
			payload, err := toPayload(raw)
			var ev *event.Event
			if err == nil {
				ev, err = event.Parse(payload)
			}
			if err == nil && ev.Timestamp.IsZero() {
				err = errors.New("no timestamp")
			}
			if err != nil {
				im.report.Events.Skipped++
				im.report.errorf("an event of %s can't be read: %v", issueName(is), err)
				return true, nil
			}
			out = append(out, ev)
			return len(out) < im.opts.Events, nil
		})
	slices.SortStableFunc(out, func(a, b *event.Event) int { return b.Timestamp.Compare(a.Timestamp) })
	return out, err
}

// mapStatus pasa el estado de Sentry al de aquí: ignored es silenced y
// resuelto en una release guarda esa release; en la siguiente, la del
// último evento, que es con la que se compara al llegar uno nuevo.
func mapStatus(is apiIssue, lastRelease string) (status, fixedIn, resolvedIn string) {
	switch is.Status {
	case "resolved":
		if is.StatusDetails.InRelease != "" {
			return "resolved", string(is.StatusDetails.InRelease), ""
		}
		var next bool
		if json.Unmarshal(is.StatusDetails.InNextRelease, &next) == nil && next && lastRelease != "" {
			return "resolved_next", "", lastRelease
		}
		return "resolved", "", ""
	case "ignored", "muted", "archived":
		return "silenced", "", ""
	}
	return "open", "", ""
}

func actionWord(a store.ImportAction, dry bool) string {
	switch {
	case a == store.ImportCreated && dry:
		return "would be created"
	case a == store.ImportCreated:
		return "created"
	case a == store.ImportUpdated && dry:
		return "would be updated"
	case a == store.ImportUpdated:
		return "updated"
	}
	return "unchanged"
}
