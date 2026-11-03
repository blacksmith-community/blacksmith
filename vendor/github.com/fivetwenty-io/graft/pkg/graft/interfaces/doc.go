// Package interfaces defines the core contracts for the graft parser layer.
//
// This package contains the interface definitions used throughout the graft
// library to ensure loose coupling between components. The interfaces define
// contracts for parsing, tokenization, AST construction, and document handling.
//
// # Key Interfaces
//
// The package defines interfaces for:
//
//   - Parser: Unified parser for operator expressions
//   - Tokenizer: Lexical analysis with position tracking
//   - AST nodes: Expression tree with visitor pattern support
//   - Document: Core document representation
//
// # Design Principles
//
// All interfaces follow these principles:
//
//   - Small, focused interfaces (Interface Segregation)
//   - Position tracking for meaningful error messages
//   - Context-aware operations for cancellation support
//   - Thread-safe contracts where applicable
package interfaces
