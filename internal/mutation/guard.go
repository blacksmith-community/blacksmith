// Package mutation holds the broker-wide guard that keeps two operations
// from changing the same service instance at once.
//
// Upgrades, certificate work, provisioning, deprovisioning, and the
// deployment actions in the UI all reserve an instance through one Guard
// before they ask the director to change it. A reservation is keyed by the
// instance ID and by the deployment name, so a caller that only knows the
// deployment still collides with one that knows both.
package mutation

import (
	"errors"
	"sync"
	"time"
)

// HolderKind names the kind of operation that holds a reservation.
type HolderKind string

// The kinds of operation that reserve an instance.
const (
	KindUpgrade          HolderKind = "upgrade"
	KindCertificates     HolderKind = "certificates"
	KindConverge         HolderKind = "converge"
	KindProvision        HolderKind = "provision"
	KindUpdate           HolderKind = "update"
	KindDeprovision      HolderKind = "deprovision"
	KindDeploymentAction HolderKind = "deployment-action"
)

// Holder describes the operation that holds a reservation.
type Holder struct {
	Kind        HolderKind `json:"kind"`
	TaskID      string     `json:"task_id,omitempty"`
	Description string     `json:"description"`
	Since       time.Time  `json:"since"`
}

// ErrInstanceBusy is matched by every BusyError.
var ErrInstanceBusy = errors.New("instance busy")

// errNoKey is returned when a caller names neither an instance nor a
// deployment, which would reserve nothing.
var errNoKey = errors.New("mutation guard needs an instance ID or a deployment name")

// BusyError reports that another operation holds the instance.
type BusyError struct {
	Holder Holder
}

// Error names the operation that holds the instance.
func (e *BusyError) Error() string {
	return "instance is busy: " + e.Holder.Description
}

// Is makes errors.Is(err, ErrInstanceBusy) true for every BusyError.
func (e *BusyError) Is(target error) bool {
	return target == ErrInstanceBusy
}

type reservation struct {
	token      uint64
	instanceID string
	deployment string
	holder     Holder
}

// Guard keeps one reservation per instance and per deployment. Its zero
// value is not usable; build one with NewGuard.
type Guard struct {
	now func() time.Time

	mu          sync.Mutex
	next        uint64
	instances   map[string]*reservation
	deployments map[string]*reservation
}

// NewGuard returns an empty guard that stamps each reservation with now.
func NewGuard(now func() time.Time) *Guard {
	if now == nil {
		now = time.Now
	}

	return &Guard{
		now:         now,
		instances:   make(map[string]*reservation),
		deployments: make(map[string]*reservation),
	}
}

// Acquire reserves the instance and its deployment for holder. It never blocks:
// when either key is already held it returns a *BusyError naming the
// current holder. An empty instance ID reserves the deployment alone, and
// an empty deployment name reserves the instance alone.
//
// The returned release frees exactly this reservation. Calling it again,
// or after someone else has reserved the same keys, does nothing.
func (g *Guard) Acquire(instanceID, deployment string, holder Holder) (func(), error) {
	if instanceID == "" && deployment == "" {
		return nil, errNoKey
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	if current := g.lookupLocked(instanceID, deployment); current != nil {
		return nil, &BusyError{Holder: current.holder}
	}

	if holder.Since.IsZero() {
		holder.Since = g.now()
	}

	g.next++
	res := &reservation{
		token:      g.next,
		instanceID: instanceID,
		deployment: deployment,
		holder:     holder,
	}

	if instanceID != "" {
		g.instances[instanceID] = res
	}

	if deployment != "" {
		g.deployments[deployment] = res
	}

	var once sync.Once

	return func() {
		once.Do(func() { g.release(res) })
	}, nil
}

// Holder reports the operation that holds the instance or its deployment.
func (g *Guard) Holder(instanceID, deployment string) (Holder, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()

	res := g.lookupLocked(instanceID, deployment)
	if res == nil {
		return Holder{}, false
	}

	return res.holder, true
}

// Snapshot returns a copy of every reservation, keyed by instance ID, or by
// deployment name for a reservation that names no instance.
func (g *Guard) Snapshot() map[string]Holder {
	g.mu.Lock()
	defer g.mu.Unlock()

	out := make(map[string]Holder, len(g.instances)+len(g.deployments))

	for id, res := range g.instances {
		out[id] = res.holder
	}

	for name, res := range g.deployments {
		if res.instanceID == "" {
			out[name] = res.holder
		}
	}

	return out
}

func (g *Guard) lookupLocked(instanceID, deployment string) *reservation {
	if instanceID != "" {
		if res, ok := g.instances[instanceID]; ok {
			return res
		}
	}

	if deployment != "" {
		if res, ok := g.deployments[deployment]; ok {
			return res
		}
	}

	return nil
}

// release frees res only where it is still the current reservation, so a
// stale release never frees a later holder of the same keys.
func (g *Guard) release(res *reservation) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if res.instanceID != "" {
		if cur, ok := g.instances[res.instanceID]; ok && cur.token == res.token {
			delete(g.instances, res.instanceID)
		}
	}

	if res.deployment != "" {
		if cur, ok := g.deployments[res.deployment]; ok && cur.token == res.token {
			delete(g.deployments, res.deployment)
		}
	}
}
