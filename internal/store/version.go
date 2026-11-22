package store

import (
	"cmp"
	"strconv"
	"strings"
)

type version struct {
	pkg  string
	nums []int
	pre  []string
}

// parseVersion lee «[paquete@][v]1.2.3[-pre][+build]», con dos o más números.
func parseVersion(s string) (version, bool) {
	var v version
	if at := strings.LastIndex(s, "@"); at >= 0 {
		v.pkg, s = s[:at], s[at+1:]
	}
	s = strings.TrimPrefix(strings.TrimPrefix(s, "v"), "V")
	s, _, _ = strings.Cut(s, "+")
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
	// Una pre-release va antes que la versión sin ella.
	switch {
	case len(va.pre) == 0 && len(vb.pre) == 0:
		return 0, true
	case len(va.pre) == 0:
		return 1, true
	case len(vb.pre) == 0:
		return -1, true
	}
	for i := range min(len(va.pre), len(vb.pre)) {
		x, y := va.pre[i], vb.pre[i]
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
			return c, true
		}
	}
	return cmp.Compare(len(va.pre), len(vb.pre)), true
}
