package operators

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"

	"github.com/fivetwenty-io/graft/internal/errtext"
	"github.com/fivetwenty-io/graft/internal/yamlcodec"
	"github.com/fivetwenty-io/graft/internal/yamlprep"
	"github.com/fivetwenty-io/graft/pkg/graft"
	"github.com/fivetwenty-io/graft/pkg/graft/tree"
)

// LoadOperator is invoked with (( load <location> )).
type LoadOperator struct{}

// Setup ...
func (LoadOperator) Setup() error {
	return nil
}

// Phase ...
func (LoadOperator) Phase() OperatorPhase {
	return EvalPhase
}

// Dependencies ...
func (LoadOperator) Dependencies(_ *Evaluator, _ []*Expr, _ []*tree.Cursor, auto []*tree.Cursor) []*tree.Cursor {
	return auto
}

// Run ...
func (LoadOperator) Run(ev *Evaluator, args []*Expr) (*Response, error) {
	DEBUG("running (( load ... )) operation at $.%s", ev.Here)
	defer DEBUG("done with (( load ... )) operation at $%s\n", ev.Here)

	if len(args) != 1 {
		return nil, fmt.Errorf("load operator requires exactly one literal string or reference argument")
	}

	// Use ResolveOperatorArgument to support nested expressions. A literal
	// never chooses which file or URL is read, so an argument that
	// resolves from one is refused before anything is opened or fetched.
	val, err := resolveRefusingLiteral(ev, args[0], "load", "a file or URL")
	if err != nil {
		DEBUG("  arg[0]: failed to resolve expression to a concrete value")
		DEBUG("     [0]: error was: %s", err)
		return nil, err
	}

	if val == nil {
		DEBUG("  arg[0]: resolved to nil")
		return nil, fmt.Errorf("load operator argument cannot be nil")
	}

	var location string
	switch v := val.(type) {
	case string:
		DEBUG("  arg[0]: using string value '%v'", v)
		location = v

	case int, int64, float64, bool:
		DEBUG("  arg[0]: converting %T to string", v)
		location = fmt.Sprintf("%v", v)

	case map[string]interface{}, []interface{}:
		DEBUG("  arg[0]: %v is not a string scalar", v)
		return nil, notAStringScalar(args[0])

	default:
		DEBUG("  arg[0]: using value of type %T as string", val)
		location = fmt.Sprintf("%v", val)
	}

	bytes, err := getBytesFromLocation(location)
	if err != nil {
		return nil, errtext.WithPlain(err, loadErrorPlain(err))
	}

	// Loaded content gets the merge's guards, since a manifest can name
	// any file. Only its first document is read, nesting past 10,000
	// levels fails before anything tokenizes it, and an alias inside its
	// own anchor's collection fails instead of decoding as null.
	bytes, err = graft.FirstMergeDocument(bytes)
	if err != nil {
		return nil, err
	}

	// Loaded content goes through the same yamlprep rewrites ParseYAML
	// applies, so an unquoted {{...}} placeholder reads as a string.
	prepared, _ := yamlprep.Prepare(bytes)
	if err := graft.CheckSelfContainingAnchors(prepared); err != nil {
		return nil, fmt.Errorf("failed to parse YAML: %w", err)
	}

	// The loaded file reads its explicit tags and its YAML 1.1 boolean
	// words the way a merge input does.
	root, err := yamlcodec.DecodeFirstGeneric(prepared)
	if err != nil {
		var tagValue *yamlcodec.TagValueError
		if errors.As(err, &tagValue) {
			return nil, fmt.Errorf("failed to parse YAML: %w", err)
		}
		return nil, errUnsupportedLoadRoot
	}
	switch v := root.(type) {
	case map[string]interface{}:
		return &Response{
			Type:  Replace,
			Value: graft.NormalizeMap(v),
		}, nil
	case []interface{}:
		return &Response{
			Type:  Replace,
			Value: v,
		}, nil
	}
	return nil, errUnsupportedLoadRoot
}

// errUnsupportedLoadRoot is the load operator's error for a file whose
// first document is neither a map nor a list, or that does not parse.
var errUnsupportedLoadRoot = errors.New("unsupported root type in loaded content, only map or list roots are supported")

// loadLocationError is a load failure whose message names the location.
// Its Error() text leaves the location out, and its detailed text keeps
// it. The error it describes names the location too, so it stays off
// the Unwrap chain, and errors.Is and errors.As look into it the way
// errtext.WithPlain's errors do.
type loadLocationError struct {
	err   error
	plain string
}

func (e *loadLocationError) Error() string         { return e.plain }
func (e *loadLocationError) DetailedError() string { return errtext.Detail(e.err) }
func (e *loadLocationError) Is(target error) bool  { return errors.Is(e.err, target) }
func (e *loadLocationError) As(target any) bool    { return errtext.AsHidden(e.err, target) }

// loadErrorPlain is the plain text of a failure to read a location. The
// errors the load operator builds already have one. A request or read
// failure from the network or the file system names the location, so
// only a generic phrase is given for it.
func loadErrorPlain(err error) string {
	var located *loadLocationError
	if errors.As(err, &located) {
		return located.plain
	}
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		return "unable to read the location: " + pathErr.Op + ": " + pathErr.Err.Error()
	}
	return "unable to get any content using the location"
}

func getBytesFromLocation(location string) ([]byte, error) {
	// Handle location as a URI if it looks like one and has a scheme
	if locURL, err := url.ParseRequestURI(location); err == nil && locURL.Scheme != "" {
		// #nosec G107 - load operator needs to fetch from user-specified URLs
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, location, http.NoBody)
		if err != nil {
			return nil, err
		}
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, err
		}
		defer func() { _ = response.Body.Close() }()

		data, err := io.ReadAll(response.Body)
		if response.StatusCode != http.StatusOK {
			return nil, &loadLocationError{err: fmt.Errorf("failed to retrieve data from location %s: %s", location, string(data)), plain: "failed to retrieve data from the location"}
		}

		return data, err
	}

	// Prepend the optional GRAFT_FILE_BASE_PATH (falling back to
	// SPRUCE_FILE_BASE_PATH) override for relative paths.
	location = resolveWithFileBasePath(location)

	// Handle location as local file if there is a file at that location
	if _, err := os.Stat(location); err == nil {
		// #nosec G304 - File path is from user-provided configuration/input which is expected behavior for the load operator
		return os.ReadFile(location)
	}

	// In any other case, bail out ...
	return nil, &loadLocationError{err: fmt.Errorf("unable to get any content using location %s: it is not a file or usable URI", location), plain: "unable to get any content using the location: it is not a file or usable URI"}
}

//nolint:gochecknoinits // Operator registration must happen at package load time
func init() {
	RegisterOp("load", LoadOperator{})
}
