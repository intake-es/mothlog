//go:build !unix

package db

import "os"

// lockFile solo crea el fichero: fuera de Unix no hay flock.
func lockFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
}
