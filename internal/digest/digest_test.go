package digest

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/intake-es/intake-errors/internal/digest/digesttest"
	"github.com/intake-es/intake-errors/internal/store"
	"github.com/intake-es/intake-errors/internal/store/storetest"
)

const base = "https://errors.example.com"

func build(t *testing.T, db *storetest.DB, projectID int64, lang string, end time.Time) *Digest {
	t.Helper()
	c := store.DefaultDigest(projectID)
	c.Language, c.Timezone = lang, "Europe/Madrid"
	d, err := Build(context.Background(), db.Store, Options{Config: c, Start: end.Add(-24 * time.Hour), End: end, Base: base})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func section(t *testing.T, d *Digest, id string) Section {
	t.Helper()
	for _, s := range d.Sections {
		if s.ID == id {
			return s
		}
	}
	t.Fatalf("sin la sección %s: %+v", id, d.Sections)
	return Section{}
}

func TestBuildKnownData(t *testing.T) {
	db := storetest.NewDB(t)
	p := db.AddProject(t, "tienda")
	end := time.Date(2026, 11, 22, 8, 0, 0, 0, time.UTC)
	is := digesttest.Seed(t, db, p.ID, end)
	d := build(t, db, p.ID, Spanish, end)

	var ids []string
	for _, s := range d.Sections {
		ids = append(ids, s.ID)
	}
	if got := strings.Join(ids, ","); got != "totals,new,regressed,growing,crons,health" {
		t.Fatalf("secciones = %s", got)
	}
	if d.Title != "Resumen diario de tienda" || d.Period != "Del sáb 21 nov, 09:00 al dom 22 nov, 09:00 (Europe/Madrid)" || d.Empty {
		t.Errorf("cabecera = %q · %q · vacío %v", d.Title, d.Period, d.Empty)
	}
	totals := section(t, d, "totals")
	want := []Stat{
		{"Eventos", "20", "+300 % respecto al periodo anterior"},
		{"Errores con eventos", "4", ""},
		{"Usuarios afectados", "3", ""},
		{"Resueltos", "1", ""},
	}
	if b, w := mustJSON(totals.Stats), mustJSON(want); b != w {
		t.Errorf("totales = %s\nquiero %s", b, w)
	}
	items := func(id string) []Item { return section(t, d, id).Items }
	if got := items("new"); len(got) != 1 || got[0].Title != "TypeError: Cannot read properties of undefined (reading 'total')" ||
		got[0].Detail != "3 eventos · checkout/cart.ts" || got[0].URL != base+"/issues/"+itoa(is.New) {
		t.Errorf("nuevos = %+v", got)
	}
	if got := items("regressed"); len(got) != 1 || got[0].Detail != "2 eventos · con web@1.4.1 · billing/invoice.py in render" ||
		got[0].URL != base+"/issues/"+itoa(is.Regressed) {
		t.Errorf("regresiones = %+v", got)
	}
	if got := items("growing"); len(got) != 1 || got[0].Title != "TimeoutError: upstream took too long" ||
		got[0].Detail != "12 eventos (antes 2) · api/search.go in Query" {
		t.Errorf("crecen = %+v", got)
	}
	crons := items("crons")
	if len(crons) != 2 || crons[0].Title != "Copia de seguridad" || crons[0].Detail != "copia-seguridad · 2 fallidos · ahora: va bien" ||
		crons[0].URL != base+"/monitors/"+itoa(p.ID)+"/copia-seguridad" ||
		crons[1].Detail != "enviar-facturas · 1 sin llegar · ahora: sin llegar" {
		t.Errorf("crons = %+v", crons)
	}
	health := section(t, d, "health")
	if health.Title != "Salud de la release web@1.4.1" || mustJSON(health.Stats) != mustJSON([]Stat{
		{"Sesiones sin crash", "98,5 %", "3 con crash"}, {"Usuarios sin crash", "75 %", "1 con crash"}, {"Sesiones", "200", ""},
	}) || len(health.Items) != 1 || health.Items[0].URL != base+"/releases/"+itoa(p.ID)+"/web@1.4.1" {
		t.Errorf("salud = %+v", health)
	}
	if s := d.Subject(); s != "Resumen diario de tienda: 1 nuevo, 1 ha vuelto, 2 crons con problemas" {
		t.Errorf("asunto = %q", s)
	}

	en := build(t, db, p.ID, English, end)
	if en.Title != "Daily digest for tienda" || en.Period != "From Sat, Nov 21, 09:00 to Sun, Nov 22, 09:00 (Europe/Madrid)" {
		t.Errorf("en: %q · %q", en.Title, en.Period)
	}
	if got := section(t, en, "totals").Stats[0]; got.Detail != "+300% vs. the previous period" {
		t.Errorf("en totales = %+v", got)
	}
	if got := section(t, en, "growing").Items[0].Detail; got != "12 events (2 before) · api/search.go in Query" {
		t.Errorf("en crecen = %q", got)
	}
	if got := section(t, en, "health").Stats[0].Value; got != "98.5%" {
		t.Errorf("en salud = %q", got)
	}
	if s := en.Subject(); s != "Daily digest for tienda: 1 new, 1 regression, 2 cron problems" {
		t.Errorf("en asunto = %q", s)
	}
	for _, f := range []string{en.Markdown(), en.Text(), en.HTML(), mustJSON(en.Slack())} {
		for _, es := range []string{"eventos", "Errores nuevos", "Han vuelto", "Resumen", "sin crash", "Abrir"} {
			if strings.Contains(f, es) {
				t.Errorf("en inglés queda %q:\n%s", es, f)
			}
		}
	}
}

func TestEmptySectionsAndQuietPeriod(t *testing.T) {
	db := storetest.NewDB(t)
	p := db.AddProject(t, "tienda")
	end := time.Date(2026, 11, 22, 8, 0, 0, 0, time.UTC)

	d := build(t, db, p.ID, Spanish, end)
	if !d.Empty || len(d.Sections) != 0 {
		t.Fatalf("sin datos = %+v", d.Sections)
	}
	if s := d.Subject(); s != "Resumen diario de tienda: sin novedades" {
		t.Errorf("asunto = %q", s)
	}
	for _, f := range []string{d.Markdown(), d.Text(), d.HTML()} {
		if !strings.Contains(f, "Sin novedades en el periodo") {
			t.Errorf("no dice que no hay nada:\n%s", f)
		}
	}

	// Solo un error que se queda igual: salen los totales y nada más.
	id := db.AddIssue(t, storetest.Issue{ProjectID: p.ID, Title: "igual", FirstSeen: end.Add(-100 * time.Hour)})
	for _, at := range []time.Time{end.Add(-30 * time.Hour), end.Add(-2 * time.Hour)} {
		db.AddEvent(t, p.ID, id, eventAt(), at)
	}
	d = build(t, db, p.ID, Spanish, end)
	if len(d.Sections) != 1 || d.Sections[0].ID != "totals" || d.Empty {
		t.Fatalf("secciones = %+v", d.Sections)
	}
	md := d.Markdown()
	for _, no := range []string{"Errores nuevos", "Han vuelto", "crecen", "Crons", "Salud"} {
		if strings.Contains(md, no) {
			t.Errorf("sale la sección vacía %q:\n%s", no, md)
		}
	}
}

func TestRenderFormats(t *testing.T) {
	db := storetest.NewDB(t)
	p := db.AddProject(t, "tienda <b>")
	end := time.Date(2026, 11, 22, 8, 0, 0, 0, time.UTC)
	digesttest.Seed(t, db, p.ID, end)
	d := build(t, db, p.ID, Spanish, end)

	md := d.Markdown()
	for _, w := range []string{
		"# Resumen diario de tienda &lt;b&gt;\n",
		"## Errores nuevos\n",
		"- [TypeError: Cannot read properties of undefined (reading 'total')](" + base + "/issues/",
		"- Eventos: **20** (+300 % respecto al periodo anterior)\n",
		"## Salud de la release web@1.4.1\n",
	} {
		if !strings.Contains(md, w) {
			t.Errorf("Markdown sin %q:\n%s", w, md)
		}
	}
	html := d.HTML()
	if strings.Contains(html, "<b>") || !strings.Contains(html, "tienda &lt;b&gt;") || !strings.Contains(html, `<html lang="es">`) {
		t.Errorf("HTML sin escapar el nombre:\n%s", html)
	}
	if strings.Contains(html, "border-left:3px") || strings.Contains(html, "<script") {
		t.Error("el correo no lleva franjas laterales ni scripts")
	}
	text := d.Text()
	if !strings.Contains(text, "ERRORES NUEVOS\n- TypeError") || !strings.Contains(text, base+"/projects/"+itoa(p.ID)+"/settings/digest") {
		t.Errorf("texto:\n%s", text)
	}

	msg := d.Slack()
	if msg.Blocks[0].Type != "header" || msg.Blocks[0].Text.Text != "Resumen diario de tienda <b>" || len(msg.Blocks) > maxBlocks {
		t.Errorf("Slack: %s", mustJSON(msg.Blocks[:2]))
	}
	if !strings.Contains(msg.Text, "tienda &lt;b&gt;") || !strings.Contains(mustJSON(msg), "|TypeError: Cannot read") {
		t.Errorf("Slack: %s", mustJSON(msg))
	}
	pl := d.Webhook("outbox-7", end)
	if pl.Type != "digest" || pl.Delivery != "outbox-7" || pl.Markdown != md || len(pl.Sections) != 6 ||
		pl.Period.Timezone != "Europe/Madrid" || !pl.Period.End.Equal(end) {
		t.Errorf("webhook = %+v", pl)
	}
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func TestUptimeSection(t *testing.T) {
	db := storetest.NewDB(t)
	p := db.AddProject(t, "tienda")
	end := time.Date(2026, 11, 22, 8, 0, 0, 0, time.UTC)
	start := end.Add(-24 * time.Hour)
	ctx := context.Background()
	monitor := func(slug, name, status string) int64 {
		var id int64
		if err := db.Pool().QueryRow(ctx, `INSERT INTO uptime_monitors (project_id, slug, name, url, status)
			VALUES ($1, $2, $3, 'https://tienda.example.com/', $4) RETURNING id`, p.ID, slug, name, status).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	incident := func(monitor int64, from time.Time, to *time.Time) {
		if _, err := db.Pool().Exec(ctx, `INSERT INTO uptime_incidents (monitor_id, started_at, ended_at)
			VALUES ($1, $2, $3)`, monitor, from, to); err != nil {
			t.Fatal(err)
		}
	}
	at := func(d time.Duration) *time.Time { v := end.Add(d); return &v }

	web := monitor("web", "Web", "up")
	// Empezó antes del periodo: solo cuentan los 10 minutos de dentro.
	incident(web, start.Add(-time.Hour), at(-24*time.Hour+10*time.Minute))
	incident(web, end.Add(-3*time.Hour), at(-3*time.Hour+45*time.Second))
	api := monitor("api", "API", "down")
	// Sigue caída al acabar el periodo: cuenta hasta el final.
	incident(api, end.Add(-2*time.Hour), nil)
	viejo := monitor("viejo", "Viejo", "up")
	incident(viejo, start.Add(-3*time.Hour), at(-25*time.Hour))

	d := build(t, db, p.ID, Spanish, end)
	s := section(t, d, "uptime")
	if s.Title != "Webs que no respondieron" || s.Total != 2 || len(s.Items) != 2 {
		t.Fatalf("sección = %+v", s)
	}
	if s.Items[0].Title != "API" || s.Items[0].Detail != "1 caída · 2 h sin responder · ahora: caída" ||
		s.Items[0].URL != base+"/monitors/"+itoa(p.ID)+"/uptime/api" {
		t.Errorf("primera = %+v", s.Items[0])
	}
	if s.Items[1].Title != "Web" || s.Items[1].Detail != "2 caídas · 10 min sin responder · ahora: responde" {
		t.Errorf("segunda = %+v", s.Items[1])
	}
	if got := d.Subject(); !strings.Contains(got, "2 webs caídas") {
		t.Errorf("asunto = %q", got)
	}
	if en := build(t, db, p.ID, English, end); section(t, en, "uptime").Items[0].Detail != "1 incident · 2 h down · now: down" {
		t.Errorf("en inglés = %+v", section(t, en, "uptime").Items[0])
	}
}
