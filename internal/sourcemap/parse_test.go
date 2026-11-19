package sourcemap

import "testing"

func TestCleanSource(t *testing.T) {
	for _, tc := range []struct{ root, in, want string }{
		{"", "src/app/a.ts", "src/app/a.ts"},
		{"", "webpack:///./src/app/a.ts", "src/app/a.ts"},
		{"", "webpack:///src/app/a.ts", "src/app/a.ts"},
		{"", "webpack://app/./src/app/a.ts", "src/app/a.ts"},
		{"", "././node_modules/x/y.js", "node_modules/x/y.js"},
		{"webpack:///", "./src/a.ts", "src/a.ts"},
		{"src/", "a.ts", "src/a.ts"},
	} {
		if got := cleanSource(tc.root, tc.in); got != tc.want {
			t.Errorf("cleanSource(%q, %q) = %q, quiero %q", tc.root, tc.in, got, tc.want)
		}
	}
}

func TestParseAndLookup(t *testing.T) {
	// Línea 1: col 0 -> a.ts 0:0, col 10 -> a.ts 2:4, col 20 sin original.
	// Línea 2: col 5 -> b.ts 0:0 (los índices relativos siguen de la línea anterior).
	m, err := Parse([]byte(`{"version":3,"sources":["a.ts","b.ts"],"sourcesContent":["x\ny\n  z\r\n"],"names":["n"],"mappings":"AAAA,UAEI,U;KCFJA"}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		line, col int
		want      Original
		ok        bool
	}{
		{0, 0, Original{"a.ts", 0, 0}, true},
		{0, 9, Original{"a.ts", 0, 0}, true},
		{0, 10, Original{"a.ts", 2, 4}, true},
		{0, 15, Original{"a.ts", 2, 4}, true},
		{0, 20, Original{}, false},
		{1, 4, Original{}, false},
		{1, 5, Original{"b.ts", 0, 0}, true},
		{2, 0, Original{}, false},
	} {
		got, ok := m.Lookup(tc.line, tc.col)
		if ok != tc.ok || got != tc.want {
			t.Errorf("Lookup(%d, %d) = %+v %v; quiero %+v %v", tc.line, tc.col, got, ok, tc.want, tc.ok)
		}
	}
	if lines := m.SourceLines("a.ts"); len(lines) != 4 || lines[2] != "  z" {
		t.Errorf("líneas de a.ts = %q", lines)
	}
	if lines := m.SourceLines("b.ts"); lines != nil {
		t.Errorf("b.ts no tiene contenido: %q", lines)
	}
}

func TestParseInvalid(t *testing.T) {
	for _, b := range []string{
		``,
		`[]`,
		`{"sources":[],"mappings":""}`,
		`{"version":3,"sources":[]}`,
		`{"version":3,"sections":[]}`,
		`{"version":3,"sources":["a"],"mappings":"AA"}`,
		`{"version":3,"sources":["a"],"mappings":"AAAA,,AAAA"}`,
		`{"version":3,"sources":["a"],"mappings":"AAAAAAA"}`,
		`{"version":3,"sources":["a"],"mappings":"AADA"}`,
		`{"version":3,"sources":["a"],"mappings":"gggggggggA"}`,
	} {
		if _, err := Parse([]byte(b)); err == nil {
			t.Errorf("Parse(%s) debería fallar", b)
		}
	}
}
