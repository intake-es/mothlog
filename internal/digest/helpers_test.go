package digest

import (
	"strconv"

	"github.com/intake-es/intake-errors/internal/event"
)

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func eventAt() event.Event { return event.Event{Level: "error", Message: "fallo"} }
