package main

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime/debug"
	"testing"
)

func TestContainerMemoryLimit(t *testing.T) {
	for _, tc := range []struct {
		name, file, content string
		want                int64
	}{
		{"cgroup v2", "memory.max", "268435456\n", 268435456},
		{"cgroup v2 sin límite", "memory.max", "max\n", 0},
		{"cgroup v1", "memory/memory.limit_in_bytes", "536870912\n", 536870912},
		{"cgroup v1 sin límite", "memory/memory.limit_in_bytes", "9223372036854771712\n", 0},
		{"sin cgroup", "", "", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if tc.file != "" {
				p := filepath.Join(root, tc.file)
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte(tc.content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if got := containerMemoryLimit(root); got != tc.want {
				t.Errorf("límite %d; quiero %d", got, tc.want)
			}
		})
	}
}

func TestSetMemoryLimit(t *testing.T) {
	prev := debug.SetMemoryLimit(-1)
	t.Cleanup(func() { debug.SetMemoryLimit(prev) })
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "memory.max"), []byte("268435456\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	t.Setenv("GOMEMLIMIT", "100MiB")
	debug.SetMemoryLimit(prev)
	setMemoryLimit(log, root)
	if got := debug.SetMemoryLimit(-1); got != prev {
		t.Errorf("con GOMEMLIMIT dado cambia el límite a %d", got)
	}

	t.Setenv("GOMEMLIMIT", "")
	setMemoryLimit(log, root)
	if got, want := debug.SetMemoryLimit(-1), int64(268435456/100*memLimitPercent); got != want {
		t.Errorf("GOMEMLIMIT %d; quiero %d", got, want)
	}
}
