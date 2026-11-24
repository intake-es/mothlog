package lang

import "testing"

func TestParse(t *testing.T) {
	for in, want := range map[string]Lang{"": EN, "en": EN, "es": ES} {
		if got, err := Parse(in); err != nil || got != want {
			t.Errorf("Parse(%q) = %q, %v", in, got, err)
		}
	}
	for _, in := range []string{"ES", "fr", "es-ES"} {
		if _, err := Parse(in); err == nil {
			t.Errorf("Parse(%q) no falla", in)
		}
	}
}

func TestT(t *testing.T) {
	if EN.T("a", "b") != "a" || ES.T("a", "b") != "b" || Lang("").T("a", "b") != "a" {
		t.Error("T no elige el idioma")
	}
	if ES.N(1, "event", "events", "evento", "eventos") != "evento" || EN.N(2, "event", "events", "evento", "eventos") != "events" {
		t.Error("N no elige el plural")
	}
}
