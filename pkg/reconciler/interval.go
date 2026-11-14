package reconciler

import (
	"errors"
	"strconv"
	"time"
)

// ErrInvalidInterval is returned for a value that is neither a Go duration
// nor a number of seconds. The error leaves the value out, so callers name
// the value and where it came from.
var ErrInvalidInterval = errors.New("not a Go duration or a number of seconds")

// ParseInterval reads a reconciler interval written either as a Go duration
// ("5m", "1h30m") or as bare seconds (3600, 60.5), which is how a YAML
// integer arrives once it is stored as a string.
func ParseInterval(value string) (time.Duration, error) {
	interval, err := time.ParseDuration(value)
	if err == nil {
		return interval, nil
	}

	seconds, numErr := strconv.ParseFloat(value, 64)
	if numErr == nil {
		return time.Duration(seconds * float64(time.Second)), nil
	}

	return 0, ErrInvalidInterval
}
