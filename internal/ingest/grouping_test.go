package ingest

import (
	"cmp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/intake-es/mothlog/internal/event"
)

func ptr[T any](v T) *T { return &v }

func excEvent(line int) *event.Event {
	return &event.Event{
		Transaction: "/pedidos",
		Exceptions: []event.Exception{
			{Type: "KeyError", Value: "'causa'"},
			{Type: "ZeroDivisionError", Value: "division by zero", Frames: []event.Frame{
				{Module: "starlette.routing", Function: "handle", Line: 10, InApp: ptr(false)},
				{Module: "app.pedidos", Function: "total", Line: line, Column: line, InApp: ptr(true)},
				{Module: "app.util", Function: "dividir", Line: line + 1, InApp: ptr(true)},
			}},
		},
	}
}

func TestNormalizeMessage(t *testing.T) {
	a := `task failed id=88dfc065281046dfa0098d0fa91bbdf4 name='enviar_factura' worker=2fb81653 try=4 duration_ms=146906 error=TimeoutError('timed out')`
	b := `task failed id=fceb1a3e2ad94b5f8244e34b6d0f4d35 name='enviar_factura' worker=8e00938f try=3 duration_ms=12 error=TimeoutError('timed out')`
	if NormalizeMessage(a) != NormalizeMessage(b) {
		t.Fatalf("distintos:\n%s\n%s", NormalizeMessage(a), NormalizeMessage(b))
	}
	if NormalizeMessage("took 500ms worker=abcdefab") != NormalizeMessage("took 12ms worker=fedcbafe") {
		t.Error("números pegados a letras y hex sin dígitos deben dar el mismo mensaje")
	}
	for in, want := range map[string]string{
		"pedido 0b6f3a1e-2c4d-4e5f-8a9b-0c1d2e3f4a5b sin número": "pedido <uuid> sin número",
		"hash deadbeefcafe1234 y deadbeef":                       "hash <hex> y <hex>",
		"job_deadbeef12 y 0x1234abcd":                            "job_<hex> y <hex>",
		"reintento 3 de 5 tras 1.5 s":                            "reintento <n> de <n> tras <n> s",
		"took 500ms try4":                                        "took <n>ms try<n>",
		"utf8 e index_v2":                                        "utf<n> e index_v<n>",
		"duró 12345678 ms":                                       "duró <n> ms",
		"cafe y accede":                                          "cafe y accede",
		"ai.action_reported_failure":                             "ai.action_reported_failure",
	} {
		if got := NormalizeMessage(in); got != want {
			t.Errorf("NormalizeMessage(%q) = %q, quiero %q", in, got, want)
		}
	}
}

func TestGroupException(t *testing.T) {
	g := Group(excEvent(20))
	if g.Title != "ZeroDivisionError: division by zero" || g.Type != "ZeroDivisionError" {
		t.Errorf("título %q, tipo %q", g.Title, g.Type)
	}
	if g.Culprit != "app.util in dividir" {
		t.Errorf("culprit = %q", g.Culprit)
	}
	if Group(excEvent(99)).Fingerprint != g.Fingerprint {
		t.Error("cambiar de línea o columna no debe partir el issue")
	}

	other := excEvent(20)
	other.Exceptions[1].Frames[2].Function = "otra"
	if Group(other).Fingerprint == g.Fingerprint {
		t.Error("otra función in_app debe dar otra huella")
	}
	vendor := excEvent(20)
	vendor.Exceptions[1].Frames[0].Function = "otra"
	if Group(vendor).Fingerprint != g.Fingerprint {
		t.Error("los fotogramas que no son in_app no cuentan si hay alguno in_app")
	}
	value := excEvent(20)
	value.Exceptions[1].Value = "otro mensaje"
	if Group(value).Fingerprint != g.Fingerprint {
		t.Error("el valor de la excepción no cuenta")
	}
	cause := excEvent(20)
	cause.Exceptions[0].Type = "ValueError"
	if Group(cause).Fingerprint != g.Fingerprint {
		t.Error("solo cuenta la excepción que se lanzó")
	}
}

func TestGroupWithoutInApp(t *testing.T) {
	a := excEvent(1)
	for i := range a.Exceptions[1].Frames {
		a.Exceptions[1].Frames[i].InApp = nil
	}
	b := excEvent(1)
	for i := range b.Exceptions[1].Frames {
		b.Exceptions[1].Frames[i].InApp = nil
	}
	b.Exceptions[1].Frames[0].Function = "otra"
	if Group(a).Fingerprint == Group(b).Fingerprint {
		t.Error("sin fotogramas in_app cuentan todos")
	}
	if c := Group(a).Culprit; c != "/pedidos" {
		t.Errorf("culprit sin in_app = %q, quiero la transaction", c)
	}
}

func TestGroupFingerprint(t *testing.T) {
	base := excEvent(1)
	def := Group(base).Fingerprint

	only := excEvent(1)
	only.Fingerprint = []string{"{{ default }}"}
	if Group(only).Fingerprint != def {
		t.Error("solo {{ default }} es la huella por defecto")
	}

	custom := excEvent(1)
	custom.Fingerprint = []string{"pagos", "stripe"}
	other := excEvent(50)
	other.Exceptions[1].Type = "OtroError"
	other.Fingerprint = []string{"pagos", "stripe"}
	if Group(custom).Fingerprint != Group(other).Fingerprint || Group(custom).Fingerprint == def {
		t.Error("la huella propia sustituye a la de por defecto")
	}

	mixA := excEvent(1)
	mixA.Fingerprint = []string{"{{default}}", "tenant-1"}
	mixB := excEvent(1)
	mixB.Fingerprint = []string{"{{ default }}", "tenant-2"}
	mixC := excEvent(1)
	mixC.Exceptions[1].Type = "OtroError"
	mixC.Fingerprint = []string{"{{ default }}", "tenant-1"}
	ga, gb, gc := Group(mixA).Fingerprint, Group(mixB).Fingerprint, Group(mixC).Fingerprint
	if ga == gb || ga == gc || ga == def {
		t.Error("{{ default }} combinado debe separar por el resto de valores y por la huella por defecto")
	}
}

func TestGroupMessage(t *testing.T) {
	a := &event.Event{Logger: "worker", Message: "task failed id=88dfc065281046dfa0098d0fa91bbdf4 try=4"}
	b := &event.Event{Logger: "worker", Message: "task failed id=fceb1a3e2ad94b5f8244e34b6d0f4d35 try=1"}
	c := &event.Event{Logger: "otro", Message: a.Message}
	if Group(a).Fingerprint != Group(b).Fingerprint {
		t.Error("mismo mensaje normalizado, mismo issue")
	}
	if Group(a).Fingerprint == Group(c).Fingerprint {
		t.Error("otro logger, otro issue")
	}
	if Group(a).Title != a.Message {
		t.Errorf("título = %q", Group(a).Title)
	}

	long := &event.Event{Exceptions: []event.Exception{{Type: "ValueError", Value: strings.Repeat("á", 500)}}}
	if n := len([]rune(Group(long).Title)); n != maxTitle {
		t.Errorf("título de %d caracteres", n)
	}
	if Group(&event.Event{}).Title == "" {
		t.Error("título vacío")
	}
}

func TestTransition(t *testing.T) {
	// Las releases se vieron en este orden; «r9» no está en la tabla.
	order := []string{"r0", "r1", "r2", "r3", "r4"}
	// Las rN no se leen como versiones; las web@ sí, y van por su número.
	compare := func(a, b string) (int, bool, bool, error) {
		if strings.HasPrefix(a, "web@") && strings.HasPrefix(b, "web@") {
			a, _, _ = strings.Cut(a, "+")
			b, _, _ = strings.Cut(b, "+")
			return strings.Compare(a, b), true, true, nil
		}
		ia, ib := slices.Index(order, a), slices.Index(order, b)
		if ia < 0 || ib < 0 {
			return 0, false, false, nil
		}
		return cmp.Compare(ia, ib), true, false, nil
	}
	for _, tc := range []struct {
		status, resolvedIn, fixedIn, release string
		// created: la release la crea el propio evento.
		created   bool
		want      string
		regressed bool
	}{
		{"open", "", "", "r1", false, "open", false},
		{"resolved", "", "", "", false, "open", true},
		{"resolved", "", "", "r4", true, "open", true},
		{"resolved_next", "r1", "", "r1", false, "resolved_next", false},
		{"resolved_next", "r1", "", "", false, "resolved_next", false},
		{"resolved_next", "r1", "", "r2", false, "open", true},
		{"resolved_next", "r1", "", "r0", false, "resolved_next", false},
		{"resolved_next", "r1", "", "r9", false, "open", true},
		{"resolved_next", "r9", "", "r1", false, "open", true},
		// Una versión que trae por primera vez este evento puede ser vieja.
		{"resolved_next", "r1", "", "r4", true, "resolved_next", false},
		// Resuelto al desplegar r2: r1 es de antes; r2 y las posteriores, regresiones.
		{"resolved", "r1", "r2", "r1", false, "resolved", false},
		{"resolved", "r1", "r2", "r2", false, "open", true},
		{"resolved", "r1", "r2", "r3", false, "open", true},
		{"resolved", "r1", "r2", "r4", false, "open", true},
		{"resolved", "r1", "r2", "", false, "resolved", false},
		{"resolved", "r1", "r2", "r4", true, "resolved", false},
		// Con números de versión, el orden es el suyo, la cree o no el evento.
		{"resolved_next", "web@1.0.0", "", "web@0.9.0", false, "resolved_next", false},
		{"resolved_next", "web@1.0.0", "", "web@1.1.0", true, "open", true},
		{"resolved", "web@1.0.0", "web@1.1.0", "web@0.9.0", false, "resolved", false},
		{"resolved", "web@1.0.0", "web@1.1.0", "web@1.2.0", true, "open", true},
		// La misma versión con otro build es esa release: reabre si se resolvió con ella.
		{"resolved", "web@1.0.0", "web@1.1.0", "web@1.1.0+build.2", true, "open", true},
		{"resolved_next", "web@1.1.0", "", "web@1.1.0+build.2", true, "resolved_next", false},
		{"silenced", "", "", "r2", false, "silenced", false},
	} {
		got, reg, err := transition(tc.status, tc.resolvedIn, tc.fixedIn, tc.release, tc.created, compare)
		if err != nil || got != tc.want || reg != tc.regressed {
			t.Errorf("transition(%q, %q, %q, %q, %v) = %q, %v, %v", tc.status, tc.resolvedIn, tc.fixedIn, tc.release, tc.created, got, reg, err)
		}
	}
}

func TestRateLimiter(t *testing.T) {
	now := time.Date(2026, 11, 19, 12, 0, 15, 0, time.UTC)
	l := NewRateLimiter(3)
	l.now = func() time.Time { return now }
	if ok, _ := l.Allow(1, 2); !ok {
		t.Fatal("2 de 3")
	}
	if ok, wait := l.Allow(1, 2); ok || wait != 45*time.Second {
		t.Fatalf("4 de 3: ok %v, espera %v", ok, wait)
	}
	if ok, _ := l.Allow(1, 1); !ok {
		t.Fatal("3 de 3: lo rechazado no debe haber contado")
	}
	if ok, _ := l.Allow(2, 3); !ok {
		t.Fatal("otro proyecto lleva su cuenta")
	}
	now = now.Add(45 * time.Second)
	if ok, _ := l.Allow(1, 3); !ok {
		t.Fatal("nueva ventana")
	}
}
