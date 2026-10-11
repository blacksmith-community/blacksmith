package client

import (
	"fmt"

	"github.com/fivetwenty-io/boshapi/pkg/boshapi"
)

// checkPathSegments refuses a caller-supplied name or ID that is empty, ".",
// or "..", before anything is sent. url.PathEscape leaves the dots alone, and
// the director's router and nginx normalise them, so a request for "/releases/"
// reaches the list route and one for "/releases/.." climbs out of the
// collection. The pairs alternate between the field's name and its value.
func checkPathSegments(action string, fieldsAndValues ...string) error {
	for index := 0; index+1 < len(fieldsAndValues); index += 2 {
		field, value := fieldsAndValues[index], fieldsAndValues[index+1]

		if value == "" || value == "." || value == ".." {
			return fmt.Errorf("%s: %w; the %s is %q and nothing was sent. Pass the name or ID of the thing to reach",
				action, boshapi.ErrPathSegmentInvalid, field, value)
		}
	}

	return nil
}
