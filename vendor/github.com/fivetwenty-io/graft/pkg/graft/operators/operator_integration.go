package operators

import (
	"github.com/fivetwenty-io/graft/pkg/graft"
)

//nolint:gochecknoinits // Parser function registration must happen at package load time
func init() {
	// Register the unified parser function with the graft package
	graft.ParseOpcallFunc = parseOpcallWithParser
	// Register the engine-aware unified parser function alongside it. graft
	// cannot reach the parser directly (operators imports graft, not the
	// reverse), so this hook is the only path from ParseOpcallForEngine to
	// ParseOpcallWithParserForEngine.
	graft.ParseOpcallForEngineFunc = parseOpcallForEngineWithParser
}

// parseOpcallWithParser uses the unified Parser implementation.
func parseOpcallWithParser(phase graft.OperatorPhase, src string) (*graft.Opcall, error) {
	return graft.ParseOpcallWithParser(phase, src)
}

// parseOpcallForEngineWithParser uses the unified Parser implementation,
// threading the engine through for engine-local operator resolution.
func parseOpcallForEngineWithParser(e graft.Engine, phase graft.OperatorPhase, src string) (*graft.Opcall, error) {
	return graft.ParseOpcallWithParserForEngine(e, phase, src)
}

// ParseOpcall provides access to the parser from within the operators package.
// It delegates to the unified parser via graft.ParseOpcall.
func ParseOpcall(phase OperatorPhase, src string) (*graft.Opcall, error) {
	return graft.ParseOpcall(phase, src)
}

// Opcall is a type alias for graft.Opcall.
type Opcall = graft.Opcall
