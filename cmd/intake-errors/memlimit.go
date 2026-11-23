package main

import (
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
)

// memLimitPercent es la parte del límite de memoria del contenedor que se da
// al montón de Go; el resto queda para pilas, búferes del sistema y SQLite.
const memLimitPercent = 90

// containerMemoryLimit lee el límite de memoria del cgroup (v2 o v1) bajo
// root; 0 si no hay.
func containerMemoryLimit(root string) int64 {
	for _, f := range []string{"memory.max", "memory/memory.limit_in_bytes"} {
		b, err := os.ReadFile(filepath.Join(root, f))
		if err != nil {
			continue
		}
		n, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
		// cgroup v1 da un número enorme cuando no hay límite.
		if err != nil || n <= 0 || n >= math.MaxInt64/2 {
			return 0
		}
		return n
	}
	return 0
}

// setMemoryLimit pone GOMEMLIMIT al memLimitPercent % del límite del contenedor
// si GOMEMLIMIT no viene dado y el contenedor tiene límite.
func setMemoryLimit(log *slog.Logger, root string) {
	if os.Getenv("GOMEMLIMIT") != "" {
		return
	}
	n := containerMemoryLimit(root)
	if n == 0 {
		return
	}
	limit := n / 100 * memLimitPercent
	debug.SetMemoryLimit(limit)
	log.Info("GOMEMLIMIT según el límite del contenedor", "contenedor_bytes", n, "gomemlimit_bytes", limit)
}
