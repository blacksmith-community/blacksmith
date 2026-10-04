package broker

import (
	"context"
	"fmt"
	"runtime/debug"
	"time"

	"blacksmith/internal/credhub"
	"blacksmith/pkg/logger"
)

// deprovisionProofTimeout bounds the vault read that looks for Cloud
// Foundry's deprovision request, so a slow vault never holds a caller.
const deprovisionProofTimeout = 10 * time.Second

// credentialCleaner deletes a gone deployment's variables from the director's
// CredHub. *credhub.Cleaner implements it.
type credentialCleaner interface {
	CleanupDeployment(ctx context.Context, target credhub.Target) credhub.Result
}

// cleanupDeploymentCredentials hands a confirmed-deleted deployment to the
// credential cleaner in its own goroutine and returns at once. The cleaner's
// result is never read here, so a failure, refusal, panic, or timeout cannot
// change what the caller does next. It does nothing when no cleaner is set.
func (b *Broker) cleanupDeploymentCredentials(ctx context.Context, instanceID, deploymentName string, log logger.Logger) {
	cleaner := b.CredentialCleaner
	if cleaner == nil {
		return
	}

	b.credentialCleanups.Add(1)

	go func() {
		defer b.credentialCleanups.Done()

		defer func() {
			recovered := recover()
			if recovered != nil {
				log.Error("CredHub cleanup for deployment %s (instance %s) panicked, and nothing more was deleted for it. The deprovision itself is unaffected. Panic: %v\n%s",
					deploymentName, instanceID, recovered, debug.Stack())
			}
		}()

		runCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), credhub.RunTimeout)
		defer cancel()

		cleaner.CleanupDeployment(runCtx, credhub.Target{InstanceID: instanceID, DeploymentName: deploymentName})
	}()
}

// CleanupDeploymentCredentials is the entry point the reconciler calls for a
// deployment whose index entry it removed. It returns at once. In the
// background it checks that Cloud Foundry asked the broker to deprovision the
// instance, and only then starts the cleanup. A deployment someone deleted by
// hand keeps its variables, and a log line names what was left behind.
func (b *Broker) CleanupDeploymentCredentials(ctx context.Context, instanceID, deploymentName string) {
	if b.CredentialCleaner == nil {
		return
	}

	log := logger.Get().Named("broker")

	b.credentialCleanups.Add(1)

	go func() {
		defer b.credentialCleanups.Done()

		defer func() {
			recovered := recover()
			if recovered != nil {
				log.Error("CredHub cleanup check for deployment %s (instance %s) panicked, and nothing was deleted for it. Panic: %v\n%s",
					deploymentName, instanceID, recovered, debug.Stack())
			}
		}()

		detached := context.WithoutCancel(ctx)

		_, requested := b.hasDeprovisionRequest(detached, instanceID)
		if !requested {
			prefix := b.credentialPrefix(deploymentName)
			log.Warn("CredHub cleanup skipped for deployment %s (instance %s): the instance was not deprovisioned through the broker, because %s/metadata records no delete_requested_at. "+
				"Its director CredHub variables under %s are left in place. Likely causes are a deployment deleted by hand or a broker that failed to record Cloud Foundry's request. "+
				"If Cloud Foundry no longer has this service instance, list the variables with `credhub find -p %s` and remove each one with `credhub delete -n <name>`.",
				deploymentName, instanceID, instanceID, prefix, prefix)

			return
		}

		b.cleanupDeploymentCredentials(detached, instanceID, deploymentName, log)
	}()
}

// hasDeprovisionRequest reports whether <instanceID>/metadata records the
// time Cloud Foundry asked the broker to deprovision the instance, and that
// time. Only the OSB deprovision handler writes the field. A vault error, a
// missing record, or an unreadable time all count as no request, and the read
// gives up after deprovisionProofTimeout.
func (b *Broker) hasDeprovisionRequest(ctx context.Context, instanceID string) (time.Time, bool) {
	log := logger.Get().Named("broker")

	readCtx, cancel := context.WithTimeout(ctx, deprovisionProofTimeout)
	defer cancel()

	type answer struct {
		metadata map[string]interface{}
		exists   bool
		err      error
	}

	answers := make(chan answer, 1)

	go func() {
		var metadata map[string]interface{}

		exists, err := b.Vault.Get(readCtx, instanceID+"/metadata", &metadata)
		answers <- answer{metadata: metadata, exists: exists, err: err}
	}()

	var got answer

	select {
	case got = <-answers:
	case <-readCtx.Done():
		log.Error("could not read %s/metadata to look for Cloud Foundry's deprovision request within %s, so the instance counts as not deprovisioned through the broker: %s",
			instanceID, deprovisionProofTimeout, readCtx.Err())

		return time.Time{}, false
	}

	if got.err != nil {
		log.Error("could not read %s/metadata to look for Cloud Foundry's deprovision request, so the instance counts as not deprovisioned through the broker: %s", instanceID, got.err)

		return time.Time{}, false
	}

	if !got.exists {
		return time.Time{}, false
	}

	raw, ok := got.metadata["delete_requested_at"].(string)
	if !ok || raw == "" {
		return time.Time{}, false
	}

	requestedAt, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		log.Error("%s/metadata records delete_requested_at as %q, which is not an RFC 3339 time, so the instance counts as not deprovisioned through the broker", instanceID, raw)

		return time.Time{}, false
	}

	return requestedAt, true
}

// credentialPrefix names the CredHub path a deployment's variables live under,
// for log lines only. The cleaner builds the path it deletes from on its own,
// through the guard.
func (b *Broker) credentialPrefix(deploymentName string) string {
	director := ""
	if b.Config != nil {
		director = b.Config.CredHub.DirectorName
	}

	return fmt.Sprintf("/%s/%s/", director, deploymentName)
}
