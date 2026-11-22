package store

import "testing"

func TestCompareVersions(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want int
		ok   bool
	}{
		{"web@1.2.0", "web@1.10.0", -1, true},
		{"web@0.9.0", "web@1.1.0", -1, true},
		{"v2.0.0", "1.9.9", 1, true},
		{"2026.11.21", "2026.11.20", 1, true},
		{"api@2026.11.21-1", "api@2026.11.21", -1, true},
		{"1.0.0-rc.2", "1.0.0-rc.10", -1, true},
		{"1.0.0-alpha", "1.0.0-alpha.1", -1, true},
		{"1.0.0+build.5", "1.0.0", 0, true},
		{"1.0", "1.0.0", 0, true},
		{"web@1.0.0", "api@1.0.0", 0, false},
		{"abc123", "1.0.0", 0, false},
		{"7", "8", 0, false},
		{"prueba-upload", "prueba-inject", 0, false},
	} {
		got, ok := compareVersions(tc.a, tc.b)
		if got != tc.want || ok != tc.ok {
			t.Errorf("compareVersions(%q, %q) = %d, %v; quiero %d, %v", tc.a, tc.b, got, ok, tc.want, tc.ok)
		}
	}
}
