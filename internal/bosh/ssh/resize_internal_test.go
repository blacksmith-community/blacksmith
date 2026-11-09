package ssh

import "testing"

// fakeWindow records the arguments of window-change requests.
type fakeWindow struct {
	calls [][2]int
}

func (f *fakeWindow) WindowChange(h, w int) error {
	f.calls = append(f.calls, [2]int{h, w})

	return nil
}

type discardLogger struct{ Logger }

func (discardLogger) Infof(string, ...interface{}) {}

func TestResizeIgnoresOutOfRangeSizes(t *testing.T) {
	t.Parallel()

	for name, size := range map[string][2]int{
		"0x10":   {0, 10},
		"10x0":   {10, 0},
		"-1x5":   {-1, 5},
		"1001x5": {1001, 5},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fake := &fakeWindow{}

			err := changeWindow(fake, discardLogger{}, "id", size[0], size[1])
			if err != nil {
				t.Fatalf("changeWindow: %v", err)
			}

			if len(fake.calls) != 0 {
				t.Errorf("WindowChange called with %v, want no call", fake.calls)
			}
		})
	}
}

func TestResizeAppliesValidSize(t *testing.T) {
	t.Parallel()

	fake := &fakeWindow{}

	err := changeWindow(fake, discardLogger{}, "id", 141, 27)
	if err != nil {
		t.Fatalf("changeWindow: %v", err)
	}

	if len(fake.calls) != 1 || fake.calls[0] != [2]int{27, 141} {
		t.Errorf("WindowChange calls = %v, want one call h=27 w=141", fake.calls)
	}
}
