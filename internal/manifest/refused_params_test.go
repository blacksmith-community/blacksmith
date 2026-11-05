package manifest

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"blacksmith/internal/services"
	"blacksmith/pkg/logger"
	"blacksmith/pkg/utils"

	graftlog "github.com/fivetwenty-io/graft/log"
	"github.com/fivetwenty-io/graft/pkg/graft"
)

// refusedProbe is the request text the refused renders carry. No error
// line, log line, or record may echo it.
const refusedProbe = "probe-refused-7f3a"

// refusedTemplates put a request parameter where graft would read it as
// a path key or as the path of a secret.
//
//nolint:gochecknoglobals // constant test data
var refusedTemplates = map[string]string{
	"bracket key": "(( grab meta.sizes[meta.params.size] ))",
	"vault path":  "(( vault meta.params.path ))",
}

// refusedPlan returns a plan whose vm_type evaluates template.
func refusedPlan(template string) services.Plan {
	return services.Plan{ID: "refused-plan", Manifest: map[string]interface{}{
		"meta":    map[string]interface{}{"sizes": map[string]interface{}{"small": "vm-small"}},
		"vm_type": template,
	}}
}

// refusedParams are the request parameters the refused templates read.
func refusedParams() map[string]interface{} {
	return map[string]interface{}{"size": refusedProbe, "path": "secret/" + refusedProbe + ":key"}
}

// TestGenManifestRefusalLogsNoRequestText shows that a render graft stops
// over a request parameter still matches graft.ErrLiteralRefused, and that
// its log names the plan and the instance and holds neither the value, the
// operator text, nor the path.
//
//nolint:paralleltest // GenManifest holds a process-wide lock, and the test swaps the process logger
func TestGenManifestRefusalLogsNoRequestText(t *testing.T) {
	for name, template := range refusedTemplates {
		t.Run(name, func(t *testing.T) {
			t.Setenv("CREDENTIALS", "")

			capture := newCaptureLogger()
			previousLogger := logger.Get()
			previousStderr := graftlog.PrintStdErrf

			logger.Set(capture)

			var (
				graftMu    sync.Mutex
				graftLines []string
			)

			graftlog.PrintStdErrf = func(format string, args ...interface{}) {
				graftMu.Lock()
				defer graftMu.Unlock()

				graftLines = append(graftLines, fmt.Sprintf(format, args...))
			}

			t.Cleanup(func() {
				logger.Set(previousLogger)

				graftlog.PrintStdErrf = previousStderr
			})

			_, err := GenManifest(refusedPlan(template), "refused-instance", utils.Wrap("meta.params", refusedParams()))
			if !errors.Is(err, graft.ErrLiteralRefused) {
				t.Fatalf("the render error does not match graft.ErrLiteralRefused: %v", err)
			}

			if strings.Contains(err.Error(), refusedProbe) {
				t.Error("the error quotes the request value")
			}

			graftMu.Lock()
			log := strings.Join(append([]string{capture.output()}, graftLines...), "\n")
			graftMu.Unlock()

			for _, banned := range []string{refusedProbe, "((", "$."} {
				if strings.Contains(log, banned) {
					t.Errorf("a log line holds %q", banned)
				}
			}

			for _, named := range []string{"refused-plan", "refused-instance"} {
				if !strings.Contains(log, named) {
					t.Errorf("no log line names %s", named)
				}
			}
		})
	}
}

// refusedVaultChild is set in the environment of the test process that
// TestRefusedVaultPathReachesNoVault starts, which then runs the check.
const refusedVaultChild = "BLACKSMITH_REFUSED_VAULT_CHILD"

// TestRefusedVaultPathReachesNoVault shows that a vault path from the
// request never reaches Vault. Graft builds its Vault client once per
// process, so the check runs in a fresh test process whose client can only
// point at a server that counts its requests. A plan's own vault path
// reaches that server, which shows the count is live.
//
//nolint:paralleltest // the child sets the process environment
func TestRefusedVaultPathReachesNoVault(t *testing.T) {
	if os.Getenv(refusedVaultChild) == "" {
		cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestRefusedVaultPathReachesNoVault$", "-test.count=1") //nolint:gosec // the test runs its own binary

		cmd.Env = append(os.Environ(), refusedVaultChild+"=1")

		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("the check failed in its own process: %s\n%s", err, out)
		}

		return
	}

	var requests atomic.Int64

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(server.Close)

	t.Setenv("VAULT_ADDR", server.URL)
	t.Setenv("VAULT_TOKEN", "refused-vault-test-token")
	t.Setenv("CREDENTIALS", "")
	logger.Set(&logger.NoOpLogger{})

	_, err := GenManifest(refusedPlan(refusedTemplates["vault path"]), "refused-instance", utils.Wrap("meta.params", refusedParams()))
	if !errors.Is(err, graft.ErrLiteralRefused) {
		t.Fatalf("the render error does not match graft.ErrLiteralRefused: %v", err)
	}

	if count := requests.Load(); count != 0 {
		t.Fatalf("the refused render sent %d requests to Vault", count)
	}

	_, _ = GenManifest(refusedPlan(`(( vault "secret/refused-control:key" ))`), "refused-instance")

	if requests.Load() == 0 {
		t.Fatal("a plan's own vault path sent no request, so the count proves nothing")
	}
}
