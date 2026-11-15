package cf

import (
	"context"
	"strings"

	"blacksmith/internal/audit"
)

// The registration keys and status a change record names.
const (
	registrationKeyStatus    = "status"
	registrationKeyCreatedAt = "created_at"
	registrationKeyUpdatedAt = "updated_at"
	registrationKeyLastError = "last_error"
	statusRegistering        = "registering"
)

// SetChanges hands the handler the store that keeps a record of every CF
// endpoint change. Without one, none is kept.
func (h *Handler) SetChanges(store *audit.ChangeStore) {
	h.changes = store
}

// registrationBookkeeping reports whether a top-level registration key is
// one the broker or the page keeps for itself, rather than a field the
// operator set, so its changes are not recorded.
func registrationBookkeeping(key string) bool {
	switch key {
	case "id", registrationKeyCreatedAt, registrationKeyUpdatedAt, registrationKeyStatus, registrationKeyLastError:
		return true
	default:
		return false
	}
}

// operatorFields flattens a registration to the fields the operator set.
func operatorFields(registration map[string]interface{}) map[string]string {
	flat := audit.Flatten(registration)

	for key := range flat {
		top, _, _ := strings.Cut(key, ".")
		if registrationBookkeeping(top) {
			delete(flat, key)
		}
	}

	return flat
}

// registrationTarget names a registration in its change records by the
// name an operator knows it by, or by its ID when it has none.
func registrationTarget(registrationID string, registrations ...map[string]interface{}) string {
	for _, registration := range registrations {
		if name := getStringFromMap(registration, "name"); name != "" {
			return name
		}
	}

	return registrationID
}

// recordRegistration keeps a record of each operator field that differs
// between old and current. A secret field, such as a stored password, has
// both its values hidden by the store.
func (h *Handler) recordRegistration(ctx context.Context, change, registrationID string, old, current map[string]interface{}) {
	if h.changes == nil {
		return
	}

	h.changes.RecordDiff(ctx, audit.ChangeRecord{
		Scope: audit.ScopeEndpoints, Target: registrationTarget(registrationID, current, old), Change: change,
	}, operatorFields(old), operatorFields(current))
}

// recordRegistrationStart keeps a record of an operator starting a
// registration, which moves its status to registering.
func (h *Handler) recordRegistrationStart(ctx context.Context, registrationID string, registration map[string]interface{}) {
	h.changes.Record(ctx, audit.ChangeRecord{
		Scope: audit.ScopeEndpoints, Target: registrationTarget(registrationID, registration), Change: audit.ChangeRegister,
		Key: registrationKeyStatus, Old: getStringFromMap(registration, registrationKeyStatus), New: statusRegistering,
	})
}
