package recovery

import (
	"testing"
	"time"
)

func TestParseDuration(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		want     time.Duration
		wantErr  bool
	}{
		{name: "empty string", input: "", want: 0, wantErr: false},
		{name: "go duration 5m", input: "5m", want: 5 * time.Minute, wantErr: false},
		{name: "go duration 1h", input: "1h", want: 1 * time.Hour, wantErr: false},
		{name: "go duration 30s", input: "30s", want: 30 * time.Second, wantErr: false},
		{name: "go duration 1h30m", input: "1h30m", want: 90 * time.Minute, wantErr: false},
		{name: "go duration 500ms", input: "500ms", want: 500 * time.Millisecond, wantErr: false},
		{name: "bare integer 3600 as seconds", input: "3600", want: 1 * time.Hour, wantErr: false},
		{name: "bare integer 300 as seconds", input: "300", want: 5 * time.Minute, wantErr: false},
		{name: "bare integer 60 as seconds", input: "60", want: 1 * time.Minute, wantErr: false},
		{name: "bare integer 0", input: "0", want: 0, wantErr: false},
		{name: "bare float 60.5 as seconds", input: "60.5", want: time.Duration(60.5 * float64(time.Second)), wantErr: false},
		{name: "invalid string abc", input: "abc", want: 0, wantErr: true},
		{name: "invalid string 1 hour", input: "1 hour", want: 0, wantErr: true},
		{name: "invalid string with spaces", input: "5 m", want: 0, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseDuration(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseDuration(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)

				return
			}

			if got != tt.want {
				t.Errorf("parseDuration(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestParseDuration_BareIntegerEquivalence(t *testing.T) {
	// Verify that bare integer and Go duration produce same result
	bareInt, err := parseDuration("3600")
	if err != nil {
		t.Fatalf("parseDuration(\"3600\") unexpected error: %v", err)
	}

	goDuration, err := parseDuration("1h")
	if err != nil {
		t.Fatalf("parseDuration(\"1h\") unexpected error: %v", err)
	}

	if bareInt != goDuration {
		t.Errorf("bare integer 3600 (%v) != Go duration 1h (%v)", bareInt, goDuration)
	}
}
