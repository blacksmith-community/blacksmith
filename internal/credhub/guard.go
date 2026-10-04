// Package credhub deletes the variables BOSH generated in the director's
// CredHub for a service deployment that Blacksmith deprovisioned. It can find
// credential names under a path and delete a credential by its exact name,
// and nothing else. It never reads a credential value.
package credhub

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Guard errors, one for each rule DeploymentPrefix enforces.
var (
	ErrDirectorNameInvalid      = errors.New("configured director name is empty or contains '/', '*', or '%'")
	ErrDirectorNameMismatch     = errors.New("configured director name does not equal the name the director reports in /info")
	ErrInstanceIDNotGUID        = errors.New("instance ID is not a GUID")
	ErrDeploymentNameInvalid    = errors.New("deployment name is empty, contains '/', '*', or '%', or has no plan part before the instance GUID")
	ErrDeploymentNotForInstance = errors.New("deployment name does not end with the instance GUID")
	ErrPlanNotInCatalog         = errors.New("deployment name does not start with a plan ID in the broker's catalog")
	ErrProtectedDeployment      = errors.New("deployment is protected")
)

// forbiddenNameChars can widen a CredHub path or a SQL LIKE match, so no
// director or deployment name may contain them.
const forbiddenNameChars = "/*%"

// prefixSlashes is the number of slashes in a deployment prefix,
// /<director>/<deployment>/.
const prefixSlashes = 3

var guidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// Target names the service instance and the BOSH deployment whose CredHub
// variables are to be deleted.
type Target struct {
	InstanceID     string
	DeploymentName string
}

// Policy carries everything the guard checks a Target against.
type Policy struct {
	// DirectorName is the configured credhub.director_name.
	DirectorName string
	// InfoDirectorName is the name the director reports in /info.
	InfoDirectorName string
	// PlanIDs are the plan IDs in the broker's current catalog.
	PlanIDs []string
	// ProtectedDeployments always holds the broker's own deployment.
	ProtectedDeployments []string
}

// DeploymentPrefix returns the CredHub path prefix /<director>/<deployment>/
// for a target, or an error that wraps the sentinel of the first rule the
// target breaks. A protected deployment is refused before any other rule is
// checked, so it is always reported as protected.
func DeploymentPrefix(target Target, policy Policy) (string, error) {
	if slices.Contains(policy.ProtectedDeployments, target.DeploymentName) {
		return "", fmt.Errorf("%w: %q", ErrProtectedDeployment, target.DeploymentName)
	}

	err := checkDirectorName(policy)
	if err != nil {
		return "", err
	}

	if !guidPattern.MatchString(target.InstanceID) {
		return "", fmt.Errorf("%w: %q", ErrInstanceIDNotGUID, target.InstanceID)
	}

	planID, err := planPart(target)
	if err != nil {
		return "", err
	}

	if !slices.Contains(policy.PlanIDs, planID) {
		return "", fmt.Errorf("%w: plan %q of deployment %q", ErrPlanNotInCatalog, planID, target.DeploymentName)
	}

	return "/" + policy.DirectorName + "/" + target.DeploymentName + "/", nil
}

// FilterOwned splits listed credential names into the ones that are safe to
// delete under prefix and the ones that are not. A name is owned only when it
// starts with the exact prefix, compared as a plain string, its remainder is
// one non-empty segment, and it is not under a protected prefix. A prefix that
// is not of the form /<director>/<deployment>/ owns nothing.
func FilterOwned(prefix string, names []string, policy Policy) ([]string, []string) {
	var owned, skipped []string

	validPrefix := isDeploymentPrefix(prefix)
	protected := ProtectedPrefixes(policy)

	for _, name := range names {
		if validPrefix && ownedName(prefix, name, protected) {
			owned = append(owned, name)
		} else {
			skipped = append(skipped, name)
		}
	}

	return owned, skipped
}

// ProtectedPrefixes returns /<director>/<deployment>/ for every protected
// deployment, under both the configured and the reported director name, with
// duplicates and empty names dropped.
func ProtectedPrefixes(policy Policy) []string {
	var prefixes []string

	for _, director := range []string{policy.DirectorName, policy.InfoDirectorName} {
		if director == "" {
			continue
		}

		for _, deployment := range policy.ProtectedDeployments {
			if deployment == "" {
				continue
			}

			prefix := "/" + director + "/" + deployment + "/"
			if !slices.Contains(prefixes, prefix) {
				prefixes = append(prefixes, prefix)
			}
		}
	}

	return prefixes
}

func checkDirectorName(policy Policy) error {
	if policy.DirectorName == "" || strings.ContainsAny(policy.DirectorName, forbiddenNameChars) {
		return fmt.Errorf("%w: %q", ErrDirectorNameInvalid, policy.DirectorName)
	}

	if policy.DirectorName != policy.InfoDirectorName {
		return fmt.Errorf("%w: configured %q, /info reports %q", ErrDirectorNameMismatch, policy.DirectorName, policy.InfoDirectorName)
	}

	return nil
}

// planPart applies the deployment name rules and returns the part of the name
// before -<instance GUID>.
func planPart(target Target) (string, error) {
	name := target.DeploymentName

	if name == "" || strings.ContainsAny(name, forbiddenNameChars) {
		return "", fmt.Errorf("%w: %q", ErrDeploymentNameInvalid, name)
	}

	suffix := "-" + target.InstanceID

	planID, found := strings.CutSuffix(name, suffix)
	if !found {
		if name == target.InstanceID {
			return "", fmt.Errorf("%w: %q has no plan part", ErrDeploymentNameInvalid, name)
		}

		return "", fmt.Errorf("%w: deployment %q, instance %q", ErrDeploymentNotForInstance, name, target.InstanceID)
	}

	if planID == "" {
		return "", fmt.Errorf("%w: %q has no plan part", ErrDeploymentNameInvalid, name)
	}

	return planID, nil
}

func isDeploymentPrefix(prefix string) bool {
	if !strings.HasPrefix(prefix, "/") || !strings.HasSuffix(prefix, "/") || strings.Count(prefix, "/") != prefixSlashes {
		return false
	}

	segments := strings.Split(strings.Trim(prefix, "/"), "/")

	for _, segment := range segments {
		if segment == "" || strings.ContainsAny(segment, "*%") {
			return false
		}
	}

	return len(segments) == prefixSlashes-1
}

func ownedName(prefix, name string, protected []string) bool {
	leaf, found := strings.CutPrefix(name, prefix)
	if !found || leaf == "" || strings.Contains(leaf, "/") {
		return false
	}

	for _, protectedPrefix := range protected {
		if strings.HasPrefix(name, protectedPrefix) {
			return false
		}
	}

	return true
}
