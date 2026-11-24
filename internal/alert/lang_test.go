package alert

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/intake-es/mothlog/internal/cron"
	"github.com/intake-es/mothlog/internal/event"
	"github.com/intake-es/mothlog/internal/lang"
	"github.com/intake-es/mothlog/internal/store"
)

// langPendings son avisos de cada tipo, con todos los datos que salen en el texto.
func langPendings() map[string]*pending {
	resolved := time.Date(2026, 11, 12, 16, 5, 0, 0, time.UTC)
	ev := &event.Event{
		Release: "web@1.4.0", Environment: "production",
		Exceptions: []event.Exception{{Type: "TypeError", Value: "x is undefined", Frames: []event.Frame{
			{Filename: "src/app/total.ts", Function: "leerTotal", Line: 12, InApp: yes(), ContextLine: "  return x.total;"},
		}}},
		User: &event.User{ID: "u_1834"},
	}
	expected := time.Date(2026, 11, 22, 10, 10, 0, 0, time.UTC)
	dur := int64(95_000)
	code := 502
	ended := time.Date(2026, 11, 22, 10, 3, 0, 0, time.UTC)
	down := int64(180_000)
	base := func(kind string) *pending {
		return &pending{id: 9, issueID: 42, kind: kind, project: "web", environment: "production", release: "web@1.4.0",
			eventAt: at, count: 7, title: "TypeError: x is undefined", ev: ev, link: "https://errors.test/issues/42"}
	}
	regressed := base("regressed")
	regressed.resolvedAt = &resolved
	frequency := base("frequency")
	frequency.detail = ruleDetail{Events: 50, WindowMinutes: 90}
	users := base("users")
	users.detail = ruleDetail{Users: 10}
	cronInfoFor := func(kind string) *pending {
		p := &pending{id: 10, kind: kind, project: "web", environment: "production", eventAt: at, link: "https://errors.test/monitors/1/copia",
			title: "Copia nocturna"}
		p.cron = &cronInfo{monitorID: 1, projectID: 1, slug: "copia", name: "Copia nocturna",
			schedule: cron.Schedule{Type: cron.TypeInterval, Every: 2, Unit: "hour"}, timezone: "Europe/Madrid",
			checkinMargin: 5, maxRuntime: 30, status: "error", durationMs: &dur, expectedAt: &expected, startedAt: at}
		return p
	}
	uptimeFor := func(kind string) *pending {
		p := &pending{id: 11, kind: kind, project: "web", eventAt: at, link: "https://errors.test/monitors/1/uptime/home", title: "home"}
		p.uptime = &uptimeInfo{monitorID: 2, projectID: 1, slug: "home", name: "home", url: "https://example.com/", method: "GET",
			threshold: 3, statusCode: &code, incident: store.UptimeIncident{StartedAt: at, FirstError: "status 502"}}
		if kind == store.KindUptimeUp {
			p.uptime.incident.EndedAt, p.uptime.incident.DurationMs = &ended, &down
		}
		return p
	}
	return map[string]*pending{
		"new": base("new"), "regressed": regressed, "frequency": frequency, "users": users,
		"cron_failed": cronInfoFor(store.KindCronFailed), "cron_missed": cronInfoFor(store.KindCronMissed),
		"cron_timeout": cronInfoFor(store.KindCronTimeout), "uptime_down": uptimeFor(store.KindUptimeDown),
		"uptime_up": uptimeFor(store.KindUptimeUp),
	}
}

// renderChannels es cada aviso como lo mandan Slack, Discord, Telegram, ntfy,
// el correo y, para los monitores, el Markdown de Linear y del webhook.
func renderChannels(t *testing.T, l lang.Lang) string {
	t.Helper()
	var b strings.Builder
	ps := langPendings()
	for _, name := range []string{"new", "regressed", "frequency", "users", "cron_failed", "cron_missed", "cron_timeout", "uptime_down", "uptime_up"} {
		p := ps[name]
		p.l = l
		js := func(v any) string {
			out, err := json.MarshalIndent(v, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			return string(out)
		}
		b.WriteString("=== " + name + " slack\n")
		if p.isMonitor() {
			b.WriteString(js(slackCron(p)))
		} else {
			b.WriteString(js(slackMessage(p, p.ev, p.link)))
		}
		b.WriteString("\n=== " + name + " discord\n" + js(discordMessage(p)))
		text, _ := telegramText(p)
		b.WriteString("\n=== " + name + " telegram\n" + text)
		s := summarize(p)
		_, fallback := kindText(p)
		b.WriteString("\n=== " + name + " ntfy y correo\nTitle: " + s.What + " · " + p.project + "\nSubject: " + subjectPrefix + fallback + p.project + ": " + s.Headline + "\n\n" + s.text(p.link))
		switch {
		case p.cron != nil:
			b.WriteString("=== " + name + " markdown\n" + cronMarkdown(p))
		case p.uptime != nil:
			b.WriteString("=== " + name + " markdown\n" + uptimeMarkdown(p))
		}
	}
	return b.String()
}

// Cada canal sale en inglés por defecto y, con DEFAULT_LANGUAGE=es, con los
// textos en español; testdata/avisos.es.txt se generó con el código de antes
// del cambio, que solo hablaba español, cambiando solo el nombre viejo por Mothlog.
func TestChannelTextsByLanguage(t *testing.T) {
	for _, l := range []lang.Lang{lang.EN, lang.ES} {
		got := renderChannels(t, l)
		path := filepath.Join("testdata", "avisos."+string(l)+".txt")
		if *update {
			if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if got != string(want) {
			gl, wl := strings.Split(got, "\n"), strings.Split(string(want), "\n")
			for i := range min(len(gl), len(wl)) {
				if gl[i] != wl[i] {
					t.Fatalf("%s difiere en la línea %d:\n quiero %s\n tengo  %s", path, i+1, wl[i], gl[i])
				}
			}
			t.Fatalf("%s difiere: %d líneas, quiero %d", path, len(gl), len(wl))
		}
	}
}
