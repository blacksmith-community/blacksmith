package bosh_test

import (
	"net/http"
	"strconv"
	"testing"
)

// seeOtherToTask answers the way a real director accepts a request that
// starts a task, with a 303 See Other to task redirectTaskID.
func seeOtherToTask(writer http.ResponseWriter) {
	writer.Header().Set("Location", "/tasks/"+strconv.Itoa(redirectTaskID))
	writer.WriteHeader(http.StatusSeeOther)
}

// Every call that starts a director task returns the queued task when the
// director names it in a 303, as a real director does, and never reports
// an unknown outcome.
func TestMutationsReturnTheTaskNamedByASeeOtherRedirect(t *testing.T) {
	for name, mutation := range allMutationCalls() {
		t.Run(name, func(t *testing.T) {
			server, log := newMutationDirector(t, nil, func(writer http.ResponseWriter, _ *http.Request) {
				seeOtherToTask(writer)
			})
			director := newReadAdapter(t, server, nil)

			task, err := mutation.call(t.Context(), director)
			checkRedirectTask(t, task, err)

			log.only(t)
		})
	}
}
