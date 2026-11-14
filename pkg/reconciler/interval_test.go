package reconciler_test

import (
	"errors"
	"testing"
	"time"

	"blacksmith/pkg/reconciler"
)

func TestParseIntervalAcceptsDurationsAndSeconds(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input string
		want  time.Duration
	}{
		{input: "5m", want: 5 * time.Minute},
		{input: "1h30m", want: 90 * time.Minute},
		{input: "3600", want: time.Hour},
		{input: "60.5", want: 60*time.Second + 500*time.Millisecond},
	}

	for _, tt := range tests {
		got, err := reconciler.ParseInterval(tt.input)
		if err != nil || got != tt.want {
			t.Errorf("ParseInterval(%q) = %v, %v, want %v", tt.input, got, err, tt.want)
		}
	}
}

func TestParseIntervalRejectsOtherText(t *testing.T) {
	t.Parallel()

	for _, input := range []string{"", "abc", "1 hour", "5 m"} {
		_, err := reconciler.ParseInterval(input)
		if !errors.Is(err, reconciler.ErrInvalidInterval) {
			t.Errorf("ParseInterval(%q) error = %v, want ErrInvalidInterval", input, err)
		}
	}
}
