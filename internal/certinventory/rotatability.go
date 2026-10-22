package certinventory

import (
	"fmt"
	"strings"

	"blacksmith/internal/credhub"
)

// Rotation modes.
const (
	ModeRedeploy = "redeploy"
	ModePatch    = "patch"
)

// Capabilities says what this broker and its director can do. The service
// computes it once and passes it in, so RotatabilityOf never parses a
// version itself.
type Capabilities struct {
	CredHub         credhub.CapabilityState
	CredHubReason   string
	DirectorVersion string
	// DirectorName is the name in every CredHub path the director owns.
	DirectorName      string
	ForceLatest       bool
	ForceLatestReason string
	RotationEnabled   bool
	PatchEnabled      bool
	ProbeEnabled      bool
}

// RotatabilityOf applies the conditions of spec 7.9. director is the
// CredHub director name, vmCount is the number of VMs in the instance, and
// gate is the instance's health gate, or empty when the service has none.
// Each failing condition adds one sentence to Reasons. When the certificate
// can rotate, Modes lists redeploy and, when its extra conditions hold,
// patch, and Reasons then explains only a missing patch mode.
func RotatabilityOf(c *Certificate, caps Capabilities, director string, vmCount int, gate string) Rotatability {
	var reasons []string

	if c.Versions == nil {
		reasons = append(reasons, "This certificate is not a CredHub variable, so Blacksmith cannot rotate it.")
	}

	switch caps.CredHub {
	case credhub.StateConfigured:
	case credhub.StateNotConfigured:
		reasons = append(reasons, "CredHub not configured")
	default:
		reason := "CredHub unavailable"
		if caps.CredHubReason != "" {
			reason += ": " + caps.CredHubReason
		}

		reasons = append(reasons, reason)
	}

	if c.Versions != nil && !inInstanceNamespace(c, director) {
		reasons = append(reasons, fmt.Sprintf("The name %s is not under /%s/<deployment>/, so the certificate does not belong to this instance alone.", c.Key, director))
	}

	switch c.Kind {
	case KindLeaf:
	case "":
		reasons = append(reasons, "The kind of this certificate is unknown, so Blacksmith cannot tell whether it is a leaf.")
	default:
		reasons = append(reasons, "Only a leaf certificate can be rotated, and this one is a CA or an intermediate.")
	}

	for _, f := range c.Findings {
		if f.Code == FindingSigningCAChanged {
			reasons = append(reasons, "The CA that would sign a regenerated certificate is not the CA that issued the deployed one.")

			break
		}
	}

	if !caps.ForceLatest {
		reason := caps.ForceLatestReason
		if reason == "" {
			reason = "The director does not accept latest variables."
		}

		reasons = append(reasons, reason)
	}

	if !caps.RotationEnabled {
		reasons = append(reasons, "Rotation is off on this broker (certificates.rotation.enabled).")
	}

	if len(reasons) > 0 {
		return Rotatability{Allowed: false, Reasons: reasons}
	}

	out := Rotatability{Allowed: true, Modes: []string{ModeRedeploy}}

	switch {
	case !caps.PatchEnabled:
		out.Reasons = append(out.Reasons, "Patch in place is off on this broker.")
	case vmCount > 1 && gate == "":
		out.Reasons = append(out.Reasons, "No health gate exists for this service, so patch in place is unavailable on a multi-VM instance.")
	default:
		out.Modes = append(out.Modes, ModePatch)
	}

	return out
}

// inInstanceNamespace says whether the certificate's name sits directly
// under /<director>/<deployment>/ for the deployment of its CredHub
// observation, and no other deployment holds it too.
func inInstanceNamespace(c *Certificate, director string) bool {
	if director == "" {
		return false
	}

	deployment := ""

	for _, o := range c.Observations {
		if o.Source == SourceCredHub {
			deployment = o.Owner.Deployment

			break
		}
	}

	if deployment == "" {
		return false
	}

	for _, o := range c.Owners {
		if o.Deployment != "" && o.Deployment != deployment {
			return false
		}
	}

	rest, ok := strings.CutPrefix(c.Key, "/"+director+"/"+deployment+"/")

	return ok && rest != "" && rest != "." && rest != ".." && !strings.Contains(rest, "/")
}
