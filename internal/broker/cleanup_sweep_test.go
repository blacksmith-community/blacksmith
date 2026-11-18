package broker_test

import (
	"math"
	"testing"
	"time"

	"blacksmith/internal/broker"
)

// The sweep waits the interval after its first try of a cleanup, and
// twice as long after each try after that, up to six hours, with no
// overflow however many tries there were.
func TestOwedRetryBackoffDoublesUpToSixHours(t *testing.T) {
	t.Parallel()

	for _, check := range []struct {
		interval time.Duration
		tries    int
		want     time.Duration
	}{
		{15 * time.Minute, 1, 15 * time.Minute},
		{15 * time.Minute, 2, 30 * time.Minute},
		{15 * time.Minute, 3, time.Hour},
		{15 * time.Minute, 5, 4 * time.Hour},
		{15 * time.Minute, 6, 6 * time.Hour},
		{15 * time.Minute, math.MaxInt32, 6 * time.Hour},
		{10 * time.Hour, 1, 6 * time.Hour},
		{10 * time.Hour, 4, 6 * time.Hour},
	} {
		if got := broker.OwedRetryBackoff(check.interval, check.tries); got != check.want {
			t.Errorf("OwedRetryBackoff(%v, %d) = %v, want %v", check.interval, check.tries, got, check.want)
		}
	}
}
