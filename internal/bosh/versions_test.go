package bosh_test

import (
	"testing"

	"blacksmith/internal/bosh"
)

// Versions compare part by part, by each part's number first and then by
// what follows it, and a longer version with the same leading parts is
// the newer one.
func TestCompareVersions(t *testing.T) {
	t.Parallel()

	cases := []struct {
		left, right string
		want        int
	}{
		{"1.10", "1.9", 1},
		{"1.9", "1.10", -1},
		{"315.41", "315.41", 0},
		{"10", "9+dev.1", 1},
		{"1.2", "1.2.1", -1},
		{"007", "7", 0},
		{"1a", "1b", -1},
		{queryLatest, "1", -1},
		{"1", queryLatest, 1},
		{"1.123456789012345678901234567890", "1.123456789012345678901234567889", 1},
	}

	for _, test := range cases {
		got := bosh.CompareVersions(test.left, test.right)
		if sign(got) != test.want {
			t.Errorf("CompareVersions(%q, %q) = %d, want the sign of %d", test.left, test.right, got, test.want)
		}
	}
}

func sign(value int) int {
	switch {
	case value > 0:
		return 1
	case value < 0:
		return -1
	}

	return 0
}
