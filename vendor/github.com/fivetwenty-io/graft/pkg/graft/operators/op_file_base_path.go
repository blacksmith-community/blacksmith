package operators

import (
	"os"
	"path/filepath"
)

// resolveWithFileBasePath prepends the file base path override to path when
// path is relative, matching spruce's op_file.go/op_load.go behavior.
//
// GRAFT_FILE_BASE_PATH is checked first; if it is unset or empty,
// SPRUCE_FILE_BASE_PATH is used instead so environments configured for
// spruce continue to work unchanged with (( file )) and (( load )).
// Absolute paths are returned unmodified and never combined with either
// base path, matching spruce's filepath.IsAbs short-circuit.
func resolveWithFileBasePath(path string) string {
	if filepath.IsAbs(path) {
		return path
	}

	base := os.Getenv("GRAFT_FILE_BASE_PATH")
	if base == "" {
		base = os.Getenv("SPRUCE_FILE_BASE_PATH")
	}

	return filepath.Join(base, path)
}
