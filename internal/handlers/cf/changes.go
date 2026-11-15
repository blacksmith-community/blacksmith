package cf

import (
	"context"
	"sort"

	"blacksmith/internal/audit"
)

// The registration keys and status a change record names.
const (
	registrationKeyName      = "name"
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

// registrationFormField reports whether a top-level registration key is
// a field of the registration form, which a change record holds by value.
func registrationFormField(key string) bool {
	switch key {
	case registrationKeyName, "api_url", "username", "broker_name", "auto_register":
		return true
	default:
		return false
	}
}

// diffRegistrations lists what differs between two registrations. The
// form's fields are compared and recorded by value. Any other top-level
// key a client sent is compared whole and recorded once, with both its
// values hidden, so a field the form doesn't know never shows its value
// and can't spread into a record for each key nested inside it.
func diffRegistrations(old, current map[string]interface{}) []audit.KeyChange {
	oldForm, oldOther := splitRegistration(old)
	currentForm, currentOther := splitRegistration(current)

	changes := audit.Diff(oldForm, currentForm)

	for _, change := range audit.Diff(oldOther, currentOther) {
		changes = append(changes, audit.KeyChange{Key: change.Key, Old: audit.Changed, New: audit.Changed})
	}

	sort.Slice(changes, func(left, right int) bool { return changes[left].Key < changes[right].Key })

	return changes
}

// splitRegistration returns the form's fields flattened by value, and
// every other key the operator could set as the JSON of its whole value,
// which is compared and never recorded.
func splitRegistration(registration map[string]interface{}) (map[string]string, map[string]string) {
	form := make(map[string]interface{})
	other := make(map[string]string)

	for key, value := range registration {
		switch {
		case registrationBookkeeping(key):
		case registrationFormField(key):
			form[key] = value
		default:
			other[key] = audit.Value(value)
		}
	}

	return audit.Flatten(form), other
}

// registrationTarget names a registration in its change records by the
// name an operator knows it by, or by its ID when it has none.
func registrationTarget(registrationID string, registrations ...map[string]interface{}) string {
	for _, registration := range registrations {
		if name := getStringFromMap(registration, registrationKeyName); name != "" {
			return name
		}
	}

	return registrationID
}

// recordRegistration keeps a record of each operator field that differs
// between old and current. A field the form doesn't have, such as a stored
// password, has both its values hidden.
func (h *Handler) recordRegistration(ctx context.Context, change, registrationID string, old, current map[string]interface{}) {
	if h.changes == nil {
		return
	}

	h.changes.RecordChanges(ctx, audit.ChangeRecord{
		Scope: audit.ScopeEndpoints, Target: registrationTarget(registrationID, current, old), Change: change,
	}, diffRegistrations(old, current))
}

// recordRegistrationStart keeps a record of an operator starting a
// registration, which moves its status to registering.
func (h *Handler) recordRegistrationStart(ctx context.Context, registrationID string, registration map[string]interface{}) {
	h.changes.Record(ctx, audit.ChangeRecord{
		Scope: audit.ScopeEndpoints, Target: registrationTarget(registrationID, registration), Change: audit.ChangeRegister,
		Key: registrationKeyStatus, Old: getStringFromMap(registration, registrationKeyStatus), New: statusRegistering,
	})
}
