// Package digest arma el resumen periódico de un proyecto (lo nuevo, lo que
// vuelve, lo que crece, los crons y la salud de la última release), lo
// escribe en Markdown, texto, HTML, Block Kit y JSON, y lo encola a su hora.
package digest

import (
	"context"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/intake-es/intake-errors/internal/store"
)

// MaxItems es cuántos issues o monitores salen como mucho en cada sección.
const MaxItems = 10

// Digest es el resumen de un proyecto en un periodo [Start, End).
type Digest struct {
	ProjectID int64     `json:"project_id"`
	Project   string    `json:"project"`
	Frequency string    `json:"frequency"`
	Language  string    `json:"language"`
	Start     time.Time `json:"start"`
	End       time.Time `json:"end"`
	Timezone  string    `json:"timezone"`
	Title     string    `json:"title"`
	// Period es el periodo escrito en el idioma y la zona horaria del resumen.
	Period string `json:"period"`
	// Empty es que no hay ninguna sección: el periodo no ha traído nada.
	Empty    bool      `json:"empty"`
	Sections []Section `json:"sections"`
	// URL es la interfaz y SettingsURL, los ajustes del resumen; vacías sin PUBLIC_URL.
	URL         string `json:"url,omitempty"`
	SettingsURL string `json:"settings_url,omitempty"`

	lang tr
}

// Section es una parte del resumen. Los formatos solo conocen esta forma, así
// que una sección nueva es una función más en sections.
type Section struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Note  string `json:"note,omitempty"`
	// Total es cuántos hay, también los que no caben en Items.
	Total int64  `json:"total,omitempty"`
	Stats []Stat `json:"stats,omitempty"`
	Items []Item `json:"items,omitempty"`
}

type Stat struct {
	Label  string `json:"label"`
	Value  string `json:"value"`
	Detail string `json:"detail,omitempty"`
}

type Item struct {
	Title  string `json:"title"`
	Detail string `json:"detail,omitempty"`
	URL    string `json:"url,omitempty"`
}

// Source es lo que recibe cada sección: de dónde leer, el periodo y el idioma.
type Source struct {
	Store     *store.Store
	ProjectID int64
	Start     time.Time
	End       time.Time
	// Base es PUBLIC_URL, para los enlaces; vacía, van sin ellos.
	Base string
	Lang tr
}

func (s *Source) issueURL(id int64) string {
	if s.Base == "" {
		return ""
	}
	return s.Base + "/issues/" + strconv.FormatInt(id, 10)
}

func (s *Source) monitorURL(slug string) string {
	if s.Base == "" {
		return ""
	}
	return s.Base + "/monitors/" + strconv.FormatInt(s.ProjectID, 10) + "/" + url.PathEscape(slug)
}

func (s *Source) uptimeURL(slug string) string {
	if s.Base == "" {
		return ""
	}
	return s.Base + "/monitors/" + strconv.FormatInt(s.ProjectID, 10) + "/uptime/" + url.PathEscape(slug)
}

func (s *Source) releaseURL(version string) string {
	if s.Base == "" {
		return ""
	}
	return s.Base + "/releases/" + strconv.FormatInt(s.ProjectID, 10) + "/" + url.PathEscape(version)
}

// sectionFunc arma una sección; nil es que no tiene nada y no sale.
type sectionFunc func(ctx context.Context, src *Source) (*Section, error)

// sections son las secciones del resumen, en su orden.
var sections = []sectionFunc{totalsSection, newSection, regressedSection, growingSection, cronsSection, uptimeSection, healthSection}

// Options dice qué resumen armar.
type Options struct {
	Config DigestConfig
	Start  time.Time
	End    time.Time
	Base   string
}

// DigestConfig es la configuración del proyecto que usa el resumen.
type DigestConfig = store.DigestConfig

// Build arma el resumen del proyecto en el periodo [o.Start, o.End).
func Build(ctx context.Context, st *store.Store, o Options) (*Digest, error) {
	p, err := st.ProjectByID(ctx, o.Config.ProjectID)
	if err != nil {
		return nil, err
	}
	l := tr(o.Config.Language)
	loc := o.Config.Location()
	d := &Digest{
		ProjectID: p.ID, Project: p.Name, Frequency: o.Config.Frequency, Language: string(l),
		Start: o.Start.UTC(), End: o.End.UTC(), Timezone: loc.String(), Sections: []Section{}, lang: l,
		Period: l.t("period", l.date(o.Start.In(loc)), l.date(o.End.In(loc)), loc.String()),
	}
	if d.Language == "" {
		d.Language, d.lang = Spanish, Spanish
	}
	title := "title.daily"
	if o.Config.Frequency == store.DigestWeekly {
		title = "title.weekly"
	}
	d.Title = d.lang.t(title, p.Name)
	if o.Base != "" {
		d.URL = o.Base + "/"
		d.SettingsURL = o.Base + "/projects/" + strconv.FormatInt(p.ID, 10) + "/settings/digest"
	}
	src := &Source{Store: st, ProjectID: p.ID, Start: o.Start, End: o.End, Base: o.Base, Lang: d.lang}
	for _, f := range sections {
		s, err := f(ctx, src)
		if err != nil {
			return nil, err
		}
		if s != nil {
			d.Sections = append(d.Sections, *s)
		}
	}
	d.Empty = len(d.Sections) == 0
	return d, nil
}

func totalsSection(ctx context.Context, src *Source) (*Section, error) {
	t, err := src.Store.DigestTotals(ctx, src.ProjectID, src.Start, src.End)
	if err != nil || (t.Events == 0 && t.Resolved == 0) {
		return nil, err
	}
	l := src.Lang
	return &Section{ID: "totals", Title: l.t("totals"), Stats: []Stat{
		{Label: l.t("totals.events"), Value: l.num(t.Events), Detail: l.change(t.Events, t.PreviousEvents)},
		{Label: l.t("totals.issues"), Value: l.num(t.Issues)},
		{Label: l.t("totals.users"), Value: l.num(t.Users)},
		{Label: l.t("totals.resolved"), Value: l.num(t.Resolved)},
	}}, nil
}

// issueItem es un issue con head (sus eventos), sus usuarios y lo que añada extra.
func issueItem(src *Source, is store.DigestIssue, head string, extra ...string) Item {
	l := src.Lang
	detail := head
	if is.Users > 0 {
		detail += " · " + l.n("users", is.Users)
	}
	for _, e := range extra {
		if e != "" {
			detail += " · " + e
		}
	}
	if is.Culprit != "" {
		detail += " · " + is.Culprit
	}
	return Item{Title: is.Title, Detail: detail, URL: src.issueURL(is.ID)}
}

func topNote(l tr, shown, total int, key ...string) string {
	if total > shown {
		return l.t(append(key, "top")[0], shown, total)
	}
	return ""
}

func newSection(ctx context.Context, src *Source) (*Section, error) {
	issues, total, err := src.Store.DigestNewIssues(ctx, src.ProjectID, src.Start, src.End, MaxItems)
	if err != nil || len(issues) == 0 {
		return nil, err
	}
	s := &Section{ID: "new", Title: src.Lang.t("new"), Note: topNote(src.Lang, len(issues), total), Total: int64(total)}
	for _, is := range issues {
		s.Items = append(s.Items, issueItem(src, is, src.Lang.n("events", is.Events)))
	}
	return s, nil
}

func regressedSection(ctx context.Context, src *Source) (*Section, error) {
	issues, total, err := src.Store.DigestRegressions(ctx, src.ProjectID, src.Start, src.End, MaxItems)
	if err != nil || len(issues) == 0 {
		return nil, err
	}
	s := &Section{ID: "regressed", Title: src.Lang.t("regressed"), Note: topNote(src.Lang, len(issues), total),
		Total: int64(total)}
	for _, is := range issues {
		rel := ""
		if is.Release != "" {
			rel = src.Lang.t("regressed.release", is.Release)
		}
		s.Items = append(s.Items, issueItem(src, is, src.Lang.n("events", is.Events), rel))
	}
	return s, nil
}

func growingSection(ctx context.Context, src *Source) (*Section, error) {
	issues, err := src.Store.DigestGrowing(ctx, src.ProjectID, src.Start, src.End, MaxItems)
	if err != nil || len(issues) == 0 {
		return nil, err
	}
	l := src.Lang
	s := &Section{ID: "growing", Title: l.t("growing")}
	for _, is := range issues {
		s.Items = append(s.Items, issueItem(src, is, l.t("growing.detail", l.n("events", is.Events), l.num(is.Previous))))
	}
	return s, nil
}

func cronsSection(ctx context.Context, src *Source) (*Section, error) {
	crons, err := src.Store.DigestCrons(ctx, src.ProjectID, src.Start, src.End)
	if err != nil || len(crons) == 0 {
		return nil, err
	}
	l := src.Lang
	s := &Section{ID: "crons", Title: l.t("crons"), Note: topNote(l, min(len(crons), MaxItems), len(crons), "top.crons"),
		Total: int64(len(crons))}
	for _, c := range crons[:min(len(crons), MaxItems)] {
		var parts []string
		for _, p := range []struct {
			key string
			n   int64
		}{{"crons.failed", c.Failed}, {"crons.missed", c.Missed}, {"crons.timeout", c.Timeouts}} {
			if p.n > 0 {
				parts = append(parts, l.n(p.key, p.n))
			}
		}
		if text, ok := l.dict()["status."+c.Status]; ok {
			parts = append(parts, l.t("crons.now", text))
		}
		if c.Name != c.Slug {
			parts = append([]string{c.Slug}, parts...)
		}
		detail := strings.Join(parts, " · ")
		s.Items = append(s.Items, Item{Title: c.Name, Detail: detail, URL: src.monitorURL(c.Slug)})
	}
	return s, nil
}

func uptimeSection(ctx context.Context, src *Source) (*Section, error) {
	mons, err := src.Store.DigestUptime(ctx, src.ProjectID, src.Start, src.End)
	if err != nil || len(mons) == 0 {
		return nil, err
	}
	l := src.Lang
	s := &Section{ID: "uptime", Title: l.t("uptime"), Note: topNote(l, min(len(mons), MaxItems), len(mons), "top.uptime"),
		Total: int64(len(mons))}
	for _, m := range mons[:min(len(mons), MaxItems)] {
		parts := []string{l.n("uptime.incidents", m.Incidents), l.t("uptime.down", downtime(m.DownMs))}
		if text, ok := l.dict()["uptime.now."+m.Status]; ok {
			parts = append(parts, text)
		}
		s.Items = append(s.Items, Item{Title: m.Name, Detail: strings.Join(parts, " · "), URL: src.uptimeURL(m.Slug)})
	}
	return s, nil
}

// downtime escribe una duración en ms como «45 s», «12 min» o «3 h 5 min».
func downtime(ms int64) string {
	secs := ms / 1000
	switch {
	case secs < 60:
		return strconv.FormatInt(secs, 10) + " s"
	case secs < 3600:
		return strconv.FormatInt(secs/60, 10) + " min"
	case secs%3600/60 == 0:
		return strconv.FormatInt(secs/3600, 10) + " h"
	default:
		return strconv.FormatInt(secs/3600, 10) + " h " + strconv.FormatInt(secs%3600/60, 10) + " min"
	}
}

func healthSection(ctx context.Context, src *Source) (*Section, error) {
	r, err := src.Store.LatestRelease(ctx, src.ProjectID, src.End)
	if err != nil || r == nil || r.Health.Sessions == 0 {
		return nil, err
	}
	l := src.Lang
	h := r.Health
	s := &Section{ID: "health", Title: l.t("health", r.Version)}
	if h.CrashFreeSessions != nil {
		s.Stats = append(s.Stats, Stat{Label: l.t("health.sessions"), Value: l.pct(*h.CrashFreeSessions),
			Detail: l.t("health.crashed", l.num(h.SessionsCrashed))})
	}
	if h.CrashFreeUsers != nil {
		s.Stats = append(s.Stats, Stat{Label: l.t("health.users"), Value: l.pct(*h.CrashFreeUsers),
			Detail: l.t("health.crashed", l.num(h.UsersCrashed))})
	}
	s.Stats = append(s.Stats, Stat{Label: l.t("health.total"), Value: l.num(h.Sessions)})
	if u := src.releaseURL(r.Version); u != "" {
		s.Items = append(s.Items, Item{Title: r.Version, URL: u})
	}
	return s, nil
}
