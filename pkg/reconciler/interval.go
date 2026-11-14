package reconciler

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"time"
)

// MinInterval is the shortest interval the reconciler runs at. The settings
// API rejects anything shorter, and the manager, the environment, the
// config file, and a value saved in Vault raise anything shorter to it.
const MinInterval = 10 * time.Second

// ErrInvalidInterval is returned for a value that can't be a reconciler
// interval. Its messages leave the value out, so callers name the value
// and where it came from.
var ErrInvalidInterval = errors.New("invalid interval")

// ParseInterval reads a reconciler interval written either as a Go duration
// ("5m", "1h30m") or as bare seconds (3600, 60.5), which is how a YAML
// integer arrives once it is stored as a string. The interval must be
// greater than zero, because a negative one panics in time.NewTicker, and
// bare seconds must be finite and fit in a time.Duration.
func ParseInterval(value string) (time.Duration, error) {
	interval, err := time.ParseDuration(value)
	if err != nil {
		seconds, numErr := strconv.ParseFloat(value, 64)
		if numErr != nil {
			return 0, fmt.Errorf("%w: not a Go duration or a number of seconds", ErrInvalidInterval)
		}

		nanos := seconds * float64(time.Second)
		if math.IsNaN(nanos) || math.IsInf(nanos, 0) || math.Abs(nanos) >= math.MaxInt64 {
			return 0, fmt.Errorf("%w: not a finite number of seconds that fits a duration", ErrInvalidInterval)
		}

		interval = time.Duration(nanos)
	}

	if interval <= 0 {
		return 0, fmt.Errorf("%w: must be greater than zero", ErrInvalidInterval)
	}

	return interval, nil
}
