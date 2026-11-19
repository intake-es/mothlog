package ingest

import (
	"strings"
	"testing"
	"time"

	"github.com/intake-es/intake-errors/internal/event"
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
	a := `task failed id=43ed20e1ee514256b5fd6db53d2ad712 name='enviar_factura' worker=723be757 try=4 duration_ms=190918 error=AttributeError("'NoneType' object has no attribute '__dict__'")`
	b := `task failed id=193d9a4704ab46469ffec6b691642810 name='enviar_factura' worker=f94152a2 try=3 duration_ms=12 error=AttributeError("'NoneType' object has no attribute '__dict__'")`
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
	a := &event.Event{Logger: "ardiq", Message: "task failed id=43ed20e1ee514256b5fd6db53d2ad712 try=4"}
	b := &event.Event{Logger: "ardiq", Message: "task failed id=193d9a4704ab46469ffec6b691642810 try=1"}
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
	for _, tc := range []struct {
		status, resolvedIn, release, want string
		regressed                         bool
	}{
		{"open", "", "r1", "open", false},
		{"resolved", "", "", "open", true},
		{"resolved_next", "r1", "r1", "resolved_next", false},
		{"resolved_next", "r1", "", "resolved_next", false},
		{"resolved_next", "r1", "r2", "open", true},
		{"silenced", "", "r2", "silenced", false},
	} {
		got, reg := transition(tc.status, tc.resolvedIn, tc.release)
		if got != tc.want || reg != tc.regressed {
			t.Errorf("transition(%q, %q, %q) = %q, %v", tc.status, tc.resolvedIn, tc.release, got, reg)
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
