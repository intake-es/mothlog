//go:build unix

package db

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// lockFile coge el cerrojo exclusivo de path, que dura mientras el fichero
// siga abierto: si el proceso muere, el sistema lo suelta.
func lockFile(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			who := ""
			if b, rerr := os.ReadFile(path); rerr == nil {
				if pid := strings.TrimSpace(string(b)); pid != "" {
					who = " (process " + pid + ")"
				}
			}
			return nil, fmt.Errorf("%w %s%s: with SQLite only one instance can run on the same file; "+
				"stop the other one before starting this one, or use Postgres to run several (lock at %s)",
				ErrLocked, strings.TrimSuffix(path, ".lock"), who, path)
		}
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	if err := f.Truncate(0); err == nil {
		_, _ = f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0)
	}
	return f, nil
}
