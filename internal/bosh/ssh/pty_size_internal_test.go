package ssh

import (
	"testing"

	"golang.org/x/crypto/ssh"
)

// fakePty records the arguments of the PTY request.
type fakePty struct {
	term string
	h, w int
}

func (f *fakePty) RequestPty(term string, h, w int, _ ssh.TerminalModes) error {
	f.term, f.h, f.w = term, h, w

	return nil
}

func TestSetupPTYOpensAtRequestedSize(t *testing.T) {
	t.Parallel()

	fake := &fakePty{}

	cols, rows := ptySize(&SSHOptions{WindowWidth: 141, WindowHeight: 27})

	err := setupPTY(fake, cols, rows)
	if err != nil {
		t.Fatalf("setupPTY: %v", err)
	}

	if fake.h != 27 || fake.w != 141 {
		t.Errorf("PTY requested h=%d w=%d, want h=27 w=141", fake.h, fake.w)
	}
}

func TestPTYSizeFallsBackToDefaults(t *testing.T) {
	t.Parallel()

	cases := map[string]*SSHOptions{
		"nil options":   nil,
		"missing":       {},
		"zero":          {WindowWidth: 0, WindowHeight: 0},
		"negative":      {WindowWidth: -5, WindowHeight: -1},
		"too wide":      {WindowWidth: 1001, WindowHeight: 27},
		"too tall":      {WindowWidth: 141, WindowHeight: 1001},
		"width missing": {WindowHeight: 27},
	}

	for name, opts := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fake := &fakePty{}
			cols, rows := ptySize(opts)

			err := setupPTY(fake, cols, rows)
			if err != nil {
				t.Fatalf("setupPTY: %v", err)
			}

			if fake.h != terminalRows || fake.w != terminalColumns {
				t.Errorf("PTY requested h=%d w=%d, want h=%d w=%d",
					fake.h, fake.w, terminalRows, terminalColumns)
			}
		})
	}
}

func TestPTYSizeAcceptsBounds(t *testing.T) {
	t.Parallel()

	cols, rows := ptySize(&SSHOptions{WindowWidth: 1, WindowHeight: 1000})
	if cols != 1 || rows != 1000 {
		t.Errorf("ptySize = %dx%d, want 1x1000", cols, rows)
	}
}
