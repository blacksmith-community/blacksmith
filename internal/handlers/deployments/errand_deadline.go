package deployments

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"blacksmith/internal/apierr"
	"blacksmith/internal/config"
	"blacksmith/internal/interfaces"
)

// errandLimit is the longest the broker holds a request open for an
// errand to finish, which is broker.errand_timeout. The director sets no
// limit of its own, and a smoke test of a large deployment can run for
// many minutes, so the limit can run well past the server's write
// timeout, which would otherwise close the connection with no answer at
// all.
func (h *Handler) errandLimit() time.Duration {
	if h.config == nil {
		return config.DefaultErrandTimeout
	}

	return h.config.GetBrokerConfig().ErrandWait()
}

var errErrandOutOfTime = errors.New("the errand ran out of time")

// errandContext bounds the wait for an errand and moves the connection's
// write deadline out to match. The server's write timeout runs from the
// start of the request, so an errand that runs past it would find the
// connection closed when its answer was ready, and the client would see
// a bare EOF. The deadline now ends actionDeadlineMargin after the wait
// does, which leaves time to write the answer. When the writer can't move
// its deadline, the wait stays within the server's write timeout as an
// instance action does, so the broker still writes its own answer.
func (h *Handler) errandContext(parent context.Context, writer http.ResponseWriter, logger interfaces.Logger) (context.Context, context.CancelFunc) {
	writeTimeout := h.serverWriteTimeout()
	limit := h.errandLimit()

	err := http.NewResponseController(writer).SetWriteDeadline(time.Now().Add(limit + actionDeadlineMargin))
	if err != nil {
		logger.Warning("Unable to extend the write deadline for an errand, so the wait stays within the write timeout of %d seconds: %v", int(writeTimeout/time.Second), err)

		limit = min(limit, max(writeTimeout-actionDeadlineMargin, writeTimeout/shortActionTimeoutDivisor))
	}

	cause := fmt.Errorf("%w after %v", errErrandOutOfTime, limit)

	return context.WithTimeoutCause(parent, limit, cause)
}

// writeErrandOutOfTime answers 504 with the code outcome_unknown when the
// wait for the errand ran out, because the errand may still be running.
// It reports whether it answered.
func writeErrandOutOfTime(ctx context.Context, writer http.ResponseWriter, deployment, errand string) bool {
	if !errors.Is(context.Cause(ctx), errErrandOutOfTime) {
		return false
	}

	apierr.Write(writer, apierr.New(http.StatusGatewayTimeout, apierr.CodeOutcomeUnknown,
		"errand "+errand+" on deployment "+deployment+" was still running when the broker stopped waiting, so it may finish later. Check the tasks of the deployment before you run it again."), http.StatusGatewayTimeout)

	return true
}
