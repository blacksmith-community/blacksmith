package operators

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/fivetwenty-io/graft/internal/utils/ansi"
	"github.com/fivetwenty-io/graft/pkg/graft/tree"
)

// FileOperator handles nested operator calls.
type FileOperator struct{}

// fileReadError renders spruce's exact read-failure wording while
// preserving the underlying error chain, which ClassifyError needs to
// map missing files and permission failures to E901/E902.
type fileReadError struct {
	msg   string
	cause error
}

func (e *fileReadError) Error() string { return e.msg }
func (e *fileReadError) Unwrap() error { return e.cause }

// Setup ...
func (FileOperator) Setup() error {
	return nil
}

// Phase ...
func (FileOperator) Phase() OperatorPhase {
	return EvalPhase
}

// Dependencies ...
func (FileOperator) Dependencies(_ *Evaluator, _ []*Expr, _ []*tree.Cursor, auto []*tree.Cursor) []*tree.Cursor {
	return auto
}

// Run ...
//
//nolint:gocyclo // file operator handles multiple path formats and argument types
func (FileOperator) Run(ev *Evaluator, args []*Expr) (*Response, error) {
	DEBUG("running (( file ... )) operation at $.%s", ev.Here)
	defer DEBUG("done with (( file ... )) operation at $%s\n", ev.Here)

	var filename string

	// Debug the incoming arguments
	DEBUG("file operator received %d arguments", len(args))
	for i, arg := range args {
		if arg != nil {
			DEBUG("  arg[%d]: type=%v, operator=%s", i, arg.Type, arg.Operator)
		} else {
			DEBUG("  arg[%d]: nil", i)
		}
	}

	// Argument validation and processing
	switch len(args) {
	case 1:
		// Use ResolveOperatorArgument to handle nested expressions
		val, err := ResolveOperatorArgument(ev, args[0])
		if err != nil {
			DEBUG("failed to resolve expression to a concrete value")
			DEBUG("error was: %s", err)
			return nil, err
		}

		if val == nil {
			return nil, fmt.Errorf("file operator argument resolved to nil")
		}

		switch val.(type) {
		case map[interface{}]interface{}, map[string]interface{}, []interface{}:
			DEBUG("file operator argument %v is not a string scalar", val)
			return nil, ansi.Errorf("@R{tried to read file} @c{%s}@R{, which is not a string scalar}", args[0].String())
		}

		filename = fmt.Sprintf("%v", val)
		DEBUG("using filename '%s'", filename)
	case 2:
		// Handle base path + filename
		basePath, err := ResolveOperatorArgument(ev, args[0])
		if err != nil {
			DEBUG("failed to resolve base path expression")
			DEBUG("error was: %s", err)
			return nil, err
		}

		fileName, err := ResolveOperatorArgument(ev, args[1])
		if err != nil {
			DEBUG("failed to resolve filename expression")
			DEBUG("error was: %s", err)
			return nil, err
		}

		if basePath == nil || fileName == nil {
			return nil, fmt.Errorf("file operator arguments cannot be nil")
		}

		filename = filepath.Join(fmt.Sprintf("%v", basePath), fmt.Sprintf("%v", fileName))
		DEBUG("using combined path '%s'", filename)
	default:
		DEBUG("file operator error: expected 1 or 2 args, got %d", len(args))
		for i, arg := range args {
			if arg != nil {
				DEBUG("  arg[%d] details: Type=%v, IsOperator=%v", i, arg.Type, arg.IsOperator())
			}
		}
		return nil, fmt.Errorf("file operator requires one or two string arguments")
	}

	// Prepend the optional GRAFT_FILE_BASE_PATH (falling back to
	// SPRUCE_FILE_BASE_PATH) override for relative paths.
	resolved := resolveWithFileBasePath(filename)
	if resolved != filename {
		DEBUG("resolved relative path using file base path override, final path: %s", resolved)
	}

	filename = resolved

	// Read the file
	file, err := os.ReadFile(filename) // #nosec G304 - file operator needs to read user-specified files
	if err != nil {
		DEBUG("failed to read file")
		DEBUG("error was: %s", err)
		return nil, &fileReadError{
			msg:   ansi.Sprintf("@R{tried to read file} @c{%s}@R{: could not be read - %s}", filename, err),
			cause: err,
		}
	}

	contents := string(file)
	DEBUG("file read successfully")

	return &Response{
		Type:  Replace,
		Value: contents,
	}, nil
}

//nolint:gochecknoinits // Operator registration must happen at package load time
func init() {
	RegisterOp("file", FileOperator{})
}
