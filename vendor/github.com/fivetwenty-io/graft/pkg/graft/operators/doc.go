// Package operators provides the operator implementations and unified registry for graft expressions.
//
// Operators are the core building blocks of graft's expression language,
// providing functionality like value references, string manipulation,
// secrets retrieval, and control flow.
//
// # Unified Operator Registry
//
// The UnifiedRegistry provides a thread-safe central registry for all operators.
// It maps operator names and aliases to their implementations:
//
//	registry := operators.NewRegistry()
//
//	// Register an operator
//	err := registry.Register(&operators.UnifiedOperatorEntry{
//	    Name:           "grab",
//	    Aliases:        []string{"get", "ref"},
//	    MinArgs:        1,
//	    MaxArgs:        -1,
//	    Phase:          graft.EvalPhase,
//	    Description:    "Reference values from the document tree",
//	    Implementation: &GrabOperator{},
//	})
//
//	// Look up by name or alias
//	entry, found := registry.Get("grab")
//	entry, found = registry.Get("get")  // Same result
//
//	// Get just the implementation
//	impl, found := registry.GetImplementation("grab")
//
//	// Validate argument count
//	err = registry.ValidateArgs("grab", 2)  // Returns nil if valid
//
// # Operator Categories
//
// Operators are organized into functional categories:
//
// Reference operators:
//
//   - grab: Reference values from other paths
//   - static_ips: Generate static IP addresses
//
// String operators:
//
//   - concat: Concatenate values into strings
//   - join: Join arrays with delimiters
//   - split: Split strings into arrays
//
// Logical operators:
//
//   - ||: Provide default values
//   - !: Existence checking
//
// Data operators:
//
//   - keys: Extract map keys
//   - values: Extract map values
//   - empty: Check for empty values
//
// Secrets operators:
//
//   - vault: Retrieve secrets from Vault/OpenBao
//   - awsparam: AWS Parameter Store
//   - awssecret: AWS Secrets Manager
//   - nats: NATS KV Store
//
// Arithmetic operators:
//
//   - +, -, *, /, %: Basic math operations
//
// Control flow:
//
//   - if/fi: Conditional blocks
//   - for/done: Iteration
//   - case/esac: Pattern matching
//
// # Operator Phases
//
// Operators execute in different phases of document processing:
//
//   - MergePhase: Operators that affect document structure during merging (e.g., defer, append)
//   - ParamPhase: Operators that validate required parameters before evaluation (e.g., param)
//   - EvalPhase: Standard evaluation operators that run after merging (most operators)
//
// # Thread Safety
//
// The UnifiedRegistry is fully thread-safe. All read operations use RLock,
// and write operations use Lock. Multiple goroutines can safely read from
// and write to the registry concurrently.
//
// # Registry Cloning
//
// Create isolated copies of the registry for testing or sandboxed evaluation:
//
//	clone := registry.Clone()
//	clone.Register(...)  // Doesn't affect original
//
// # Custom Operators
//
// Custom operators can be registered with the engine:
//
//	engine.RegisterOperator("myop", &MyOperator{})
//
// Or directly with a registry:
//
//	registry.Register(&operators.UnifiedOperatorEntry{
//	    Name:           "myop",
//	    MinArgs:        1,
//	    MaxArgs:        1,
//	    Phase:          graft.EvalPhase,
//	    Description:    "My custom operator",
//	    Implementation: &MyOperator{},
//	})
//
// # Operator Interface
//
// All operators implement the Operator interface:
//
//	type Operator interface {
//	    Meta() OperatorMeta
//	    Setup() error
//	    Phase() OperatorPhase
//	    Run(ctx EvalContext, args []Expression) (*Response, error)
//	    Dependencies(args []Expression) []*Cursor
//	}
package operators
