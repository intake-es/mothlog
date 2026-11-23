package main

import (
	"fmt"
	"io"
	"runtime/debug"
)

// Los pone GoReleaser con -ldflags -X.
var (
	version = "dev"
	commit  = ""
	date    = ""
)

func printVersion(out io.Writer) error {
	c, d := commit, date
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, s := range info.Settings {
			switch {
			case s.Key == "vcs.revision" && c == "":
				c = s.Value
			case s.Key == "vcs.time" && d == "":
				d = s.Value
			}
		}
	}
	if c == "" {
		c = "desconocido"
	}
	if d == "" {
		d = "desconocida"
	}
	_, err := fmt.Fprintf(out, "intake-errors %s\ncommit: %s\nfecha: %s\n", version, c, d)
	return err
}
