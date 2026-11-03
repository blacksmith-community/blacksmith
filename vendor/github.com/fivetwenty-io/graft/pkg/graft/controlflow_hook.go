package graft

// ControlFlowExpander preprocesses raw YAML source bytes before they reach
// the YAML parser, expanding (( if )), (( for )), (( while )), and
// (( case )) control-flow constructs into plain YAML. Nil means no
// preprocessing is registered.
//
// The engine parameter is the engine whose ParseYAML call is doing the
// expansion (DefaultEngine.ParseYAML passes its own receiver). It is
// threaded through to condition/iterable/subject evaluation and the
// prescan scope so a custom operator registered on that engine
// (RegisterOperator, WithCustomOperator) resolves inside (( if )),
// (( for )), (( while )), and (( case )) the same way it does everywhere
// else — without this, those constructs always evaluated against a
// nil-engine Evaluator/Parser and fell back to DefaultRegistry only. A nil
// engine argument (or an engine with no local override for a given
// operator name) still resolves identically to before.
//
// This package cannot import pkg/graft/controlflow directly: the
// preprocessor needs Evaluator and Engine types from this package to
// evaluate conditions and iterables against a prescan scope, so the
// dependency must run the other way. pkg/graft/controlflow's init()
// assigns this hook when that package is imported (mirroring how
// pkg/graft/operators registers operators via init() — see
// cmd/graft/main.go's blank import of both packages). When no consumer
// imports pkg/graft/controlflow, this variable stays nil and ParseYAML
// behaves exactly as it did before control-flow support existed.
var ControlFlowExpander func(e Engine, source []byte) ([]byte, error)
