package natsbackend

import (
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"strings"

	"github.com/nats-io/nats.go"

	"github.com/fivetwenty-io/graft/internal/errtext"
)

// redactedUserinfo stands in for the credentials redactURL removes.
const redactedUserinfo = "xxxxx"

// redactURL returns raw, a NATS URL or a comma-separated list of them,
// with its credentials replaced by redactedUserinfo. Everything from
// the first "scheme://" up to the last "@" is replaced, so no part of a
// password survives, however oddly it is written. The username goes
// too, because NATS reads a URL with a username and no password as one
// that carries a token. Every host but the last in a list that holds
// credentials goes with them. A URL with no "@" is returned whole.
func redactURL(raw string) string {
	at := strings.LastIndex(raw, "@")
	if at < 0 {
		return raw
	}
	begin := 0
	if sep := strings.Index(raw, "://"); sep > 0 && sep < at && isURLScheme(raw[:sep]) {
		begin = sep + len("://")
	}
	return raw[:begin] + redactedUserinfo + raw[at:]
}

// isURLScheme reports whether s has the form of a URL scheme.
func isURLScheme(s string) bool {
	for i, c := range s {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
		case i > 0 && (c >= '0' && c <= '9' || c == '+' || c == '-' || c == '.'):
		default:
			return false
		}
	}
	return s != ""
}

// errURLCredentials stands in for the reason a URL with credentials
// does not parse, since that reason can quote part of the password.
var errURLCredentials = errors.New("the reason is not shown because the URL holds credentials")

// redactCause returns err with the URL of a parse failure redacted.
// When that URL holds credentials, the reason it does not parse is
// dropped as well, since net/url quotes the part it rejects.
func redactCause(err error) error {
	var urlErr *url.Error
	if !errors.As(err, &urlErr) || !strings.Contains(urlErr.URL, "@") {
		return err
	}
	return &url.Error{Op: urlErr.Op, URL: redactURL(urlErr.URL), Err: errURLCredentials}
}

// knownConnectReasons are the connection errors whose text a connection
// failure's plain text may repeat, since their text is fixed and names
// nothing.
var knownConnectReasons = []error{
	nats.ErrNoServers,
	nats.ErrAuthorization,
	nats.ErrTimeout,
}

// connectError builds the error for a connection that failed after
// attempts tries. Its Error() text quotes nothing from the URL, and its
// detailed text quotes the URL with the credentials redacted.
func connectError(attempts int, err error) error {
	reason := ""
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		reason = ": the URL does not parse"
	} else {
		for _, known := range knownConnectReasons {
			if errors.Is(err, known) {
				reason = ": " + known.Error()
				break
			}
		}
	}
	return errtext.WithPlain(
		fmt.Errorf("failed to connect to NATS after %d attempts: %w", attempts, redactCause(err)),
		fmt.Sprintf("failed to connect to NATS after %d attempts%s", attempts, reason))
}

// configError builds the error for connection options that could not
// be built. Its Error() text repeats the cause's only when the cause is
// one of graft's own errors, whose Error() text quotes no value.
func configError(err error) error {
	plain := "invalid NATS connection configuration"
	if errtext.Exposable(err) {
		plain += ": " + err.Error()
	}
	return errtext.WithPlain(fmt.Errorf("invalid NATS connection configuration: %w", err), plain)
}

// fileErrorReason is the text of err, an error from loading a file the
// configuration names, without the file name: the failed operation and
// the system's reason for a *fs.PathError, and a generic phrase for
// anything else, whose text may quote the name or the file's content.
func fileErrorReason(err error) string {
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		return pathErr.Op + ": " + pathErr.Err.Error()
	}
	return "the file could not be read"
}
