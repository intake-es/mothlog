package ingest

import (
	"bytes"
	"context"
	"slices"
	"testing"
	"time"

	"github.com/intake-es/intake-errors/internal/store"
)

// withReleaseEnv devuelve el envío del navegador con otro event_id, release y
// entorno.
func (f fixture) withReleaseEnv(t *testing.T, release, env string) []byte {
	t.Helper()
	body := f.withEventID(t, newHexID())
	for old, repl := range map[string]string{`"release":"fixture-release-1"`: `"release":"` + release + `"`, `"environment":"production"`: `"environment":"` + env + `"`} {
		if !bytes.Contains(body, []byte(old)) {
			t.Fatalf("el envío no lleva %s", old)
		}
		body = bytes.ReplaceAll(body, []byte(old), []byte(repl))
	}
	return body
}

// withFingerprint es withReleaseEnv con otra huella, para que el evento vaya a
// otro issue del mismo proyecto.
func (f fixture) withFingerprint(t *testing.T, fp, release, env string) []byte {
	t.Helper()
	body := f.withReleaseEnv(t, release, env)
	// La última vez que sale es la del evento; la primera, la del trace de la cabecera.
	at := bytes.LastIndex(body, []byte(`"release":"`+release+`"`))
	if at < 0 {
		t.Fatalf("el envío no lleva la release %s", release)
	}
	return slices.Concat(body[:at], []byte(`"fingerprint":["`+fp+`"],`), body[at:])
}

func TestReleasesFromEvents(t *testing.T) {
	e := newTestEnv(t, 1000)
	ctx := context.Background()
	f := loadFixture(t, "web-typeerror-1")
	e.sendOK(t, f, f.withReleaseEnv(t, "web@1.0.0", "production"))
	e.sendOK(t, f, f.withReleaseEnv(t, "web@1.0.0", "staging"))
	e.sendOK(t, f, f.withReleaseEnv(t, "web@1.1.0", "production"))

	if n := e.count(t, `SELECT count(*) FROM releases WHERE project_id = 2`); n != 2 {
		t.Fatalf("releases = %d, quiero web@1.0.0 y web@1.1.0", n)
	}
	issues := e.issues(t)
	if len(issues) != 1 || issues[0].count != 3 {
		t.Fatalf("issues = %+v", issues)
	}
	id := issues[0].id

	page, err := e.s.ListReleases(ctx, store.ReleaseFilter{Project: "web"})
	if err != nil {
		t.Fatal(err)
	}
	rels := page.Releases
	if len(rels) != 2 || rels[0].Version != "web@1.1.0" || rels[1].Version != "web@1.0.0" {
		t.Fatalf("releases = %+v", rels)
	}
	if r := rels[1]; r.NewIssues != 1 || r.Issues != 1 || r.Events != 2 {
		t.Errorf("web@1.0.0: %d nuevos, %d issues, %d eventos", r.NewIssues, r.Issues, r.Events)
	}
	if r := rels[0]; r.NewIssues != 0 || r.Issues != 1 || r.Events != 1 {
		t.Errorf("web@1.1.0: %d nuevos, %d issues, %d eventos", r.NewIssues, r.Issues, r.Events)
	}

	d, err := e.s.ReleaseDetail(ctx, 2, "web@1.1.0", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(d.New) != 0 || len(d.Continued) != 1 || d.Continued[0].ID != id || d.Continued[0].ReleaseEvents != 1 {
		t.Errorf("web@1.1.0: nuevos %+v, siguen %+v", d.New, d.Continued)
	}
	if d.Previous == nil || *d.Previous != "web@1.0.0" || d.Next != nil {
		t.Errorf("anterior %v, siguiente %v", d.Previous, d.Next)
	}
	d, err = e.s.ReleaseDetail(ctx, 2, "web@1.0.0", "staging")
	if err != nil {
		t.Fatal(err)
	}
	if len(d.New) != 1 || d.New[0].ReleaseEvents != 1 || d.Events != 1 {
		t.Errorf("web@1.0.0 en staging: nuevos %+v, %d eventos", d.New, d.Events)
	}
	issue, err := e.s.Issue(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if issue.FirstRelease == nil || *issue.FirstRelease != "web@1.0.0" || issue.LastRelease == nil || *issue.LastRelease != "web@1.1.0" {
		t.Errorf("primera %v y última %v release", issue.FirstRelease, issue.LastRelease)
	}

	envs, err := e.s.Environments(ctx, "web")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, env := range envs {
		names = append(names, env.Name)
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"production", "staging"}) {
		t.Errorf("entornos = %v", names)
	}
	if envs, _ := e.s.Environments(ctx, "api"); len(envs) != 0 {
		t.Errorf("entornos de api = %+v", envs)
	}
}

// Resuelto en la siguiente versión: se resuelve del todo al desplegar una
// release posterior; los eventos de la vieja no lo reabren y los de la nueva
// o una posterior son una regresión que se apunta en su release.
func TestResolvedNextWithDeploy(t *testing.T) {
	e := newTestEnv(t, 1000)
	ctx := context.Background()
	f := loadFixture(t, "web-typeerror-1")
	e.sendOK(t, f, f.withReleaseEnv(t, "web@1.0.0", "production"))
	id := e.issues(t)[0].id

	if _, err := e.s.SetIssueStatus(ctx, id, "resolved_next"); err != nil {
		t.Fatal(err)
	}
	// Desplegar la misma release no lo resuelve.
	if _, resolved, err := e.s.AddDeploy(ctx, 2, "web@1.0.0", "staging", time.Now()); err != nil || len(resolved) != 0 {
		t.Fatalf("desplegar la misma release resolvió %v, %v", resolved, err)
	}
	if _, resolved, err := e.s.AddDeploy(ctx, 2, "web@1.1.0", "production", time.Now()); err != nil || !slices.Equal(resolved, []int64{id}) {
		t.Fatalf("desplegar web@1.1.0 resolvió %v, %v", resolved, err)
	}
	issue, err := e.s.Issue(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if issue.Status != "resolved" || issue.FixedInRelease == nil || *issue.FixedInRelease != "web@1.1.0" {
		t.Fatalf("tras desplegar: %s, resuelto en %v", issue.Status, issue.FixedInRelease)
	}
	d, err := e.s.ReleaseDetail(ctx, 2, "web@1.1.0", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Stopped) != 1 || d.Stopped[0].ID != id || len(d.Deploys) != 1 || d.Deploys[0].Environment != "production" {
		t.Errorf("web@1.1.0: dejaron de salir %+v, despliegues %+v", d.Stopped, d.Deploys)
	}

	// En un entorno donde nunca salió no cuenta como que dejó de salir.
	if d, err := e.s.ReleaseDetail(ctx, 2, "web@1.1.0", "staging"); err != nil || len(d.Stopped) != 0 {
		t.Errorf("web@1.1.0 en staging: dejaron de salir %+v, %v", d.Stopped, err)
	}

	// Un cliente que sigue con la release vieja no lo reabre.
	e.sendOK(t, f, f.withReleaseEnv(t, "web@1.0.0", "production"))
	if got := e.issues(t)[0]; got.status != "resolved" || got.regressed {
		t.Fatalf("con la release vieja: %s, regresión %v", got.status, got.regressed)
	}
	// Ni una versión vieja que no había mandado nada: va por su número, ni en
	// su primer evento ni en los siguientes.
	for i := range 2 {
		e.sendOK(t, f, f.withReleaseEnv(t, "web@0.9.0", "production"))
		if got := e.issues(t)[0]; got.status != "resolved" || got.regressed {
			t.Fatalf("evento %d de una versión vieja nunca vista: %s, regresión %v", i+1, got.status, got.regressed)
		}
	}

	e.sendOK(t, f, f.withReleaseEnv(t, "web@1.1.0", "production"))
	got := e.issues(t)[0]
	if got.status != "open" || !got.regressed {
		t.Fatalf("con la release desplegada: %s, regresión %v", got.status, got.regressed)
	}
	if calls := e.notes.all(); calls[len(calls)-1].outcome != Regressed {
		t.Errorf("el último aviso es %v", calls[len(calls)-1].outcome)
	}
	d, err = e.s.ReleaseDetail(ctx, 2, "web@1.1.0", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Regressed) != 1 || d.Regressed[0].ID != id || len(d.Continued) != 0 || d.Regressions != 1 {
		t.Errorf("web@1.1.0: regresiones %+v, siguen %+v", d.Regressed, d.Continued)
	}
	issue, _ = e.s.Issue(ctx, id)
	if issue.FixedInRelease != nil || issue.ResolvedInRelease != nil {
		t.Errorf("tras la regresión quedan resuelto en %v y %v", issue.FixedInRelease, issue.ResolvedInRelease)
	}
}

// Desplegar una release posterior que aún no ha mandado eventos también
// resuelve; y un evento de una release aún más nueva, creada a mano y sin
// desplegar, es una regresión.
func TestResolvedNextLaterRelease(t *testing.T) {
	e := newTestEnv(t, 1000)
	ctx := context.Background()
	f := loadFixture(t, "web-typeerror-1")
	e.sendOK(t, f, f.withReleaseEnv(t, "web@1.0.0", "production"))
	id := e.issues(t)[0].id
	if _, err := e.s.SetIssueStatus(ctx, id, "resolved_next"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.s.CreateRelease(ctx, 2, "web@2.0.0"); err != nil {
		t.Fatal(err)
	}
	if _, resolved, err := e.s.AddDeploy(ctx, 2, "web@2.0.0", "production", time.Now()); err != nil || len(resolved) != 1 {
		t.Fatalf("resueltos = %v, %v", resolved, err)
	}
	if _, _, err := e.s.CreateRelease(ctx, 2, "web@3.0.0"); err != nil {
		t.Fatal(err)
	}
	e.sendOK(t, f, f.withReleaseEnv(t, "web@3.0.0", "staging"))
	if got := e.issues(t)[0]; got.status != "open" || !got.regressed {
		t.Fatalf("con una release posterior creada a mano: %s, regresión %v", got.status, got.regressed)
	}
}

// Releases que no se leen como versiones van por cuándo se vieron: la que
// solo traen los eventos no reabre con su primer evento (puede ser de un
// cliente viejo), con el segundo sí.
func TestResolvedLaterReleaseOnlyFromEvents(t *testing.T) {
	e := newTestEnv(t, 1000)
	ctx := context.Background()
	f := loadFixture(t, "web-typeerror-1")
	e.sendOK(t, f, f.withReleaseEnv(t, "build-a1", "production"))
	id := e.issues(t)[0].id
	if _, err := e.s.SetIssueStatus(ctx, id, "resolved_next"); err != nil {
		t.Fatal(err)
	}
	if _, resolved, err := e.s.AddDeploy(ctx, 2, "build-b2", "production", time.Now()); err != nil || len(resolved) != 1 {
		t.Fatalf("resueltos = %v, %v", resolved, err)
	}
	e.sendOK(t, f, f.withReleaseEnv(t, "build-c3", "staging"))
	if got := e.issues(t)[0]; got.status != "resolved" || got.regressed {
		t.Fatalf("primer evento de build-c3: %s, regresión %v", got.status, got.regressed)
	}
	e.sendOK(t, f, f.withReleaseEnv(t, "build-c3", "staging"))
	if got := e.issues(t)[0]; got.status != "open" || !got.regressed {
		t.Fatalf("segundo evento de build-c3: %s, regresión %v", got.status, got.regressed)
	}
}

// Con números de versión, una posterior que solo traen los eventos reabre
// desde su primer evento.
func TestResolvedLaterVersionFirstEvent(t *testing.T) {
	e := newTestEnv(t, 1000)
	ctx := context.Background()
	f := loadFixture(t, "web-typeerror-1")
	e.sendOK(t, f, f.withReleaseEnv(t, "web@1.0.0", "production"))
	id := e.issues(t)[0].id
	if _, err := e.s.SetIssueStatus(ctx, id, "resolved_next"); err != nil {
		t.Fatal(err)
	}
	if _, resolved, err := e.s.AddDeploy(ctx, 2, "web@1.1.0", "production", time.Now()); err != nil || len(resolved) != 1 {
		t.Fatalf("resueltos = %v, %v", resolved, err)
	}
	e.sendOK(t, f, f.withReleaseEnv(t, "web@1.2.0", "staging"))
	if got := e.issues(t)[0]; got.status != "open" || !got.regressed {
		t.Fatalf("primer evento de web@1.2.0: %s, regresión %v", got.status, got.regressed)
	}
}

// En resolved_next, el evento de una versión vieja que nunca había mandado
// nada no lo reabre; uno de una release posterior que ya existía, sí.
func TestResolvedNextOldUnseenVersion(t *testing.T) {
	e := newTestEnv(t, 1000)
	ctx := context.Background()
	f := loadFixture(t, "web-typeerror-1")
	e.sendOK(t, f, f.withReleaseEnv(t, "web@1.0.0", "production"))
	id := e.issues(t)[0].id
	if _, err := e.s.SetIssueStatus(ctx, id, "resolved_next"); err != nil {
		t.Fatal(err)
	}
	// La versión vieja la estrena un evento de otro issue; luego llegan dos
	// al resuelto: ninguno lo reabre.
	e.sendOK(t, f, f.withFingerprint(t, "otro", "web@0.9.0", "production"))
	for i := range 2 {
		e.sendOK(t, f, f.withReleaseEnv(t, "web@0.9.0", "production"))
		got := e.issues(t)[0]
		if got.id != id || got.status != "resolved_next" || got.regressed {
			t.Fatalf("evento %d de una versión vieja nunca vista: %+v", i+1, got)
		}
	}
	if n := len(e.issues(t)); n != 2 {
		t.Fatalf("issues = %d, quiero el resuelto y el otro", n)
	}
	if _, _, err := e.s.CreateRelease(ctx, 2, "web@1.1.0"); err != nil {
		t.Fatal(err)
	}
	e.sendOK(t, f, f.withReleaseEnv(t, "web@1.1.0", "production"))
	if got := e.issues(t)[0]; got.status != "open" || !got.regressed {
		t.Fatalf("con web@1.1.0: %s, regresión %v", got.status, got.regressed)
	}
}

// Una vuelta atrás no resuelve lo que estaba en resolved_next, exista o no
// la versión. Limitación documentada en el README: con nombres que no son
// versiones, desplegar uno nunca visto cuenta como posterior y sí resuelve.
func TestRollbackDeploy(t *testing.T) {
	e := newTestEnv(t, 1000)
	ctx := context.Background()
	f := loadFixture(t, "web-typeerror-1")
	if _, _, err := e.s.CreateRelease(ctx, 2, "web@0.9.0"); err != nil {
		t.Fatal(err)
	}
	e.sendOK(t, f, f.withReleaseEnv(t, "web@1.0.0", "production"))
	id := e.issues(t)[0].id
	if _, err := e.s.SetIssueStatus(ctx, id, "resolved_next"); err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{"web@0.9.0", "web@0.8.0"} {
		if _, resolved, err := e.s.AddDeploy(ctx, 2, v, "production", time.Now()); err != nil || len(resolved) != 0 {
			t.Fatalf("volver a %s resolvió %v, %v", v, resolved, err)
		}
	}

	// Lo de antes, fuera: una release sin número contra web@1.0.0 también va
	// por cuándo se vio.
	if _, err := e.s.SetIssueStatus(ctx, id, "resolved"); err != nil {
		t.Fatal(err)
	}
	e.sendOK(t, f, f.withFingerprint(t, "sin-version", "build-a1", "production"))
	other := e.issues(t)[1].id
	if _, err := e.s.SetIssueStatus(ctx, other, "resolved_next"); err != nil {
		t.Fatal(err)
	}
	if _, resolved, err := e.s.AddDeploy(ctx, 2, "build-old", "production", time.Now()); err != nil || !slices.Equal(resolved, []int64{other}) {
		t.Fatalf("desplegar build-old, que no existía, resolvió %v, %v", resolved, err)
	}
}
