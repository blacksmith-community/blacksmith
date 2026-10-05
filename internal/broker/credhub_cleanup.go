package broker

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"time"

	"blacksmith/internal/credhub"
	"blacksmith/pkg/logger"
)

// deprovisionProofTimeout bounds the vault read that looks for Cloud
// Foundry's deprovision request, so a slow vault never holds a caller.
const deprovisionProofTimeout = 10 * time.Second

// errNoDeprovisionRequest means the instance's metadata holds no usable record
// of Cloud Foundry's deprovision request.
var errNoDeprovisionRequest = errors.New("no deprovision request is on record")

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

		_, err := b.hasDeprovisionRequest(detached, instanceID)
		if err != nil {
			prefix := b.credentialPrefix(deploymentName)

			cause := err.Error()
			if errors.Is(err, errNoDeprovisionRequest) {
				cause = "the instance was not deprovisioned through the broker, because " + instanceID + "/metadata records no delete_requested_at"
			}

			log.Warn("CredHub cleanup skipped for deployment %s (instance %s): %s. "+
				"Its director CredHub variables under %s are left in place. Likely causes are a deployment deleted by hand, a broker that failed to record Cloud Foundry's request, or a service instance GUID that was provisioned again after an earlier deprovision. "+
				"If Cloud Foundry no longer has this service instance, list the variables with `credhub find -p %s` and remove each one with `credhub delete -n <name>`.",
				deploymentName, instanceID, cause, prefix, prefix)

			return
		}

		b.cleanupDeploymentCredentials(detached, instanceID, deploymentName, log)
	}()
}

// hasDeprovisionRequest returns the time Cloud Foundry asked the broker to
// deprovision the instance, as <instanceID>/metadata records it. Only the OSB
// deprovision handler writes delete_requested_at. A vault error, a missing
// record, or an unreadable time all count as no request, and the read gives up
// after deprovisionProofTimeout. A request older than the metadata's
// created_at, which a completed provision writes, or older than its
// provision_requested_at, which Provision writes before it deploys, belongs to
// an earlier instance that used the same GUID, so it is refused too. A request
// with the same time as either stands. Metadata with neither field carries no
// later creation to compare against, and the request stands.
//
// The error wraps errNoDeprovisionRequest when nothing usable is on record,
// and says why otherwise.
func (b *Broker) hasDeprovisionRequest(ctx context.Context, instanceID string) (time.Time, error) {
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
		defer func() {
			recovered := recover()
			if recovered != nil {
				answers <- answer{err: fmt.Errorf("the vault read panicked: %v\n%s", recovered, debug.Stack())}
			}
		}()

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

		return time.Time{}, errNoDeprovisionRequest
	}

	if got.err != nil {
		log.Error("could not read %s/metadata to look for Cloud Foundry's deprovision request, so the instance counts as not deprovisioned through the broker: %s", instanceID, got.err)

		return time.Time{}, errNoDeprovisionRequest
	}

	if !got.exists {
		return time.Time{}, errNoDeprovisionRequest
	}

	raw, ok := got.metadata["delete_requested_at"].(string)
	if !ok || raw == "" {
		return time.Time{}, errNoDeprovisionRequest
	}

	requestedAt, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		log.Error("%s/metadata records delete_requested_at as %q, which is not an RFC 3339 time, so the instance counts as not deprovisioned through the broker", instanceID, raw)

		return time.Time{}, errNoDeprovisionRequest
	}

	return requestedAt, b.checkRequestFollowsCreation(instanceID, got.metadata, raw, requestedAt)
}

// requestFollowsFields are the metadata fields whose times a delete request
// must not precede. created_at is written when a provision completes, and
// provision_requested_at when one starts, so a GUID provisioned again is caught
// while its deploy is still running.
var requestFollowsFields = []string{"created_at", "provision_requested_at"}

// checkRequestFollowsCreation refuses a delete request that is older than a
// provision time in the same metadata. A request with the same time as one is
// accepted. The request is then a leftover of an earlier instance with the same
// GUID. A field that is absent is skipped, so adopted and legacy instances with
// no record keep working.
func (b *Broker) checkRequestFollowsCreation(instanceID string, metadata map[string]interface{}, rawRequest string, requestedAt time.Time) error {
	for _, field := range requestFollowsFields {
		value, present := metadata[field]
		if !present {
			continue
		}

		raw, isString := value.(string)
		if !isString || raw == "" {
			return fmt.Errorf("%s/metadata records %s as %v, which is not a time string, so the delete request cannot be shown to follow the instance's creation", instanceID, field, value)
		}

		recorded, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return fmt.Errorf("%s/metadata records %s as %q, which is not an RFC 3339 time (%w), so the delete request cannot be shown to follow the instance's creation", instanceID, field, raw, err)
		}

		if requestedAt.Before(recorded) {
			return fmt.Errorf("the delete request at %s predates the instance's %s of %s in %s/metadata, so it belongs to an earlier instance that used the same GUID",
				rawRequest, field, raw, instanceID)
		}
	}

	return nil
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
