package store

import (
	"cmp"
	"regexp"
	"strconv"
	"strings"
)

type version struct {
	pkg  string
	nums []int
	pre  []string
	// commits es el número de commits después de la etiqueta en un nombre de git describe.
	commits int
}

// describeSuffix es lo que añade git describe a la etiqueta: «-5-gabc123», con «-dirty» si acaso.
var describeSuffix = regexp.MustCompile(`-(\d+)-g[0-9a-fA-F]{4,40}(-dirty)?$`)

// parseVersion lee «[paquete@][v]1.2.3[-pre][-N-g<commit>][+build]», con dos
// o más números.
func parseVersion(s string) (version, bool) {
	var v version
	if at := strings.LastIndex(s, "@"); at >= 0 {
		v.pkg, s = s[:at], s[at+1:]
	}
	s = strings.TrimPrefix(strings.TrimPrefix(s, "v"), "V")
	s, _, _ = strings.Cut(s, "+")
	if m := describeSuffix.FindStringSubmatchIndex(s); m != nil {
		n, err := strconv.Atoi(s[m[2]:m[3]])
		if err != nil {
			return v, false
		}
		v.commits, s = n, s[:m[0]]
	}
	s, pre, hasPre := strings.Cut(s, "-")
	if hasPre {
		if pre == "" {
			return v, false
		}
		v.pre = strings.Split(pre, ".")
	}
	parts := strings.Split(s, ".")
	if len(parts) < 2 {
		return v, false
	}
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || p == "" {
			return v, false
		}
		v.nums = append(v.nums, n)
	}
	return v, true
}

// compareVersions ordena a y b por su número, como semver, si las dos se
// leen como versiones del mismo paquete; si no, ok es false.
func compareVersions(a, b string) (c int, ok bool) {
	va, okA := parseVersion(a)
	vb, okB := parseVersion(b)
	if !okA || !okB || va.pkg != vb.pkg {
		return 0, false
	}
	for i := range max(len(va.nums), len(vb.nums)) {
		x, y := 0, 0
		if i < len(va.nums) {
			x = va.nums[i]
		}
		if i < len(vb.nums) {
			y = vb.nums[i]
		}
		if c := cmp.Compare(x, y); c != 0 {
			return c, true
		}
	}
	if c := comparePre(va.pre, vb.pre); c != 0 {
		return c, true
	}
	return cmp.Compare(va.commits, vb.commits), true
}

// comparePre ordena las pre-releases: una va antes que la versión sin ella.
func comparePre(a, b []string) int {
	switch {
	case len(a) == 0 && len(b) == 0:
		return 0
	case len(a) == 0:
		return 1
	case len(b) == 0:
		return -1
	}
	for i := range min(len(a), len(b)) {
		x, y := a[i], b[i]
		nx, errX := strconv.Atoi(x)
		ny, errY := strconv.Atoi(y)
		var c int
		switch {
		case errX == nil && errY == nil:
			c = cmp.Compare(nx, ny)
		case errX == nil:
			c = -1
		case errY == nil:
			c = 1
		default:
			c = strings.Compare(x, y)
		}
		if c != 0 {
			return c
		}
	}
	return cmp.Compare(len(a), len(b))
}
