package graft

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/fivetwenty-io/graft/pkg/graft/tree"
)

// TestHelper provides utilities for testing graft operations.
type TestHelper struct {
	engine Engine
	t      *testing.T
}

// NewTestHelper creates a new test helper with default engine configuration.
func NewTestHelper(t *testing.T) *TestHelper {
	engine, err := NewEngine()
	if err != nil {
		t.Fatalf("Failed to create test engine: %v", err)
	}
	return &TestHelper{
		engine: engine,
		t:      t,
	}
}

// NewTestHelperWithOptions creates a new test helper with custom engine options.
func NewTestHelperWithOptions(t *testing.T, opts ...EngineOption) *TestHelper {
	engine, err := NewEngine(opts...)
	if err != nil {
		t.Fatalf("Failed to create test engine with options: %v", err)
	}
	return &TestHelper{
		engine: engine,
		t:      t,
	}
}

// NewTestEvaluator parses yamlStr and returns a real *Evaluator over its
// root document, wired directly to a fresh default-configured engine.
// Engine.Evaluate instead hands its evaluator a wrapper whose operator
// state belongs to that run alone. Each call here builds its own engine,
// so no other run shares the state this evaluator sees.
//
// It is the constructor to use when unit-testing a custom Operator's Run
// method directly, in place of the fictional EvalContext/NewMockEvalContext
// scheme once documented here:
// Operator.Run receives (ev *Evaluator, args []*Expr), not an EvalContext,
// and args arrive unevaluated — call EvaluateOperatorArgs(ev, args) inside
// the operator under test if it needs resolved values.
//
// The document must parse to a map at its root; NewTestEvaluator calls
// t.Fatalf and returns nil if yamlStr fails to parse or its root is not a
// map, mirroring every other Must*/New* helper in this file.
func NewTestEvaluator(t *testing.T, yamlStr string) *Evaluator {
	t.Helper()

	engine, err := NewEngine()
	if err != nil {
		t.Fatalf("NewTestEvaluator: failed to create engine: %v", err)
		return nil
	}

	doc, err := engine.ParseYAML([]byte(yamlStr))
	if err != nil {
		t.Fatalf("NewTestEvaluator: failed to parse YAML: %v\nYAML:\n%s", err, yamlStr)
		return nil
	}

	root, ok := doc.RawData().(map[string]interface{})
	if !ok {
		t.Fatalf("NewTestEvaluator: document root must be a map, got %T", doc.RawData())
		return nil
	}

	de, ok := engine.(*DefaultEngine)
	if !ok {
		t.Fatalf("NewTestEvaluator: internal error: engine is not *DefaultEngine (%T)", engine)
		return nil
	}

	return de.createEvaluator(root)
}

// ParseYAMLString parses YAML from string and returns a document.
func (h *TestHelper) ParseYAMLString(yamlStr string) Document {
	doc, err := h.engine.ParseYAML([]byte(yamlStr))
	if err != nil {
		h.t.Fatalf("Failed to parse YAML: %v\nYAML:\n%s", err, yamlStr)
	}
	return doc
}

// ParseJSONString parses JSON from string and returns a document.
func (h *TestHelper) ParseJSONString(jsonStr string) Document {
	doc, err := h.engine.ParseJSON([]byte(jsonStr))
	if err != nil {
		h.t.Fatalf("Failed to parse JSON: %v\nJSON:\n%s", err, jsonStr)
	}
	return doc
}

// MustMerge merges documents and fails the test if there's an error.
func (h *TestHelper) MustMerge(docs ...Document) Document {
	ctx := context.Background()
	result, err := h.engine.Merge(ctx, docs...).Execute()
	if err != nil {
		h.t.Fatalf("Failed to merge documents: %v", err)
	}
	return result
}

// MustMergeWithPrune merges documents with pruning and fails the test if there's an error.
func (h *TestHelper) MustMergeWithPrune(prune []string, docs ...Document) Document {
	ctx := context.Background()
	builder := h.engine.Merge(ctx, docs...)
	for _, key := range prune {
		builder = builder.WithPrune(key)
	}
	result, err := builder.Execute()
	if err != nil {
		h.t.Fatalf("Failed to merge documents with prune: %v", err)
	}
	return result
}

// MustEvaluate evaluates a document and fails the test if there's an error.
func (h *TestHelper) MustEvaluate(doc Document) Document {
	ctx := context.Background()
	result, err := h.engine.Evaluate(ctx, doc)
	if err != nil {
		h.t.Fatalf("Failed to evaluate document: %v", err)
	}
	return result
}

// MustMergeAndEvaluate merges and evaluates documents in one step.
func (h *TestHelper) MustMergeAndEvaluate(docs ...Document) Document {
	merged := h.MustMerge(docs...)
	return h.MustEvaluate(merged)
}

// AssertPath asserts that a path exists and has the expected value.
func (h *TestHelper) AssertPath(doc Document, path string, expected interface{}) {
	actual, err := doc.Get(path)
	if err != nil {
		h.t.Fatalf("Path '%s' not found: %v", path, err)
	}
	if !reflect.DeepEqual(actual, expected) {
		h.t.Fatalf("Path '%s': expected %v (%T), got %v (%T)", path, expected, expected, actual, actual)
	}
}

// AssertPathString asserts that a path exists and has the expected string value.
func (h *TestHelper) AssertPathString(doc Document, path string, expected string) {
	actual, err := doc.GetString(path)
	if err != nil {
		h.t.Fatalf("Path '%s' string value error: %v", path, err)
	}
	if actual != expected {
		h.t.Fatalf("Path '%s': expected %q, got %q", path, expected, actual)
	}
}

// AssertPathInt asserts that a path exists and has the expected int value.
func (h *TestHelper) AssertPathInt(doc Document, path string, expected int) {
	actual, err := doc.GetInt(path)
	if err != nil {
		h.t.Fatalf("Path '%s' int value error: %v", path, err)
	}
	if actual != expected {
		h.t.Fatalf("Path '%s': expected %d, got %d", path, expected, actual)
	}
}

// AssertPathBool asserts that a path exists and has the expected bool value.
func (h *TestHelper) AssertPathBool(doc Document, path string, expected bool) {
	actual, err := doc.GetBool(path)
	if err != nil {
		h.t.Fatalf("Path '%s' bool value error: %v", path, err)
	}
	if actual != expected {
		h.t.Fatalf("Path '%s': expected %t, got %t", path, expected, actual)
	}
}

// AssertPathNotExists asserts that a path does not exist.
func (h *TestHelper) AssertPathNotExists(doc Document, path string) {
	_, err := doc.Get(path)
	if err == nil {
		h.t.Fatalf("Path '%s' should not exist", path)
	}
}

// AssertError asserts that an error occurred and optionally checks the error type.
func (h *TestHelper) AssertError(err error, expectedType ...ErrorType) {
	if err == nil {
		h.t.Fatal("Expected an error but got nil")
	}

	if len(expectedType) > 0 {
		// Handle both GraftError and MultiError
		var graftErr *GraftError
		var multiErr MultiError
		switch {
		case errors.As(err, &graftErr):
			if graftErr.Type != expectedType[0] {
				h.t.Fatalf("Expected error type %v but got %v", expectedType[0], graftErr.Type)
			}
		case errors.As(err, &multiErr):
			// For MultiError, just check that it contains errors
			if len(multiErr.Errors) == 0 {
				h.t.Fatal("MultiError contains no errors")
			}
			// Accept MultiError as a valid evaluation error
			if expectedType[0] != EvaluationError {
				h.t.Fatalf("Expected error type %v but got MultiError: %v", expectedType[0], multiErr)
			}
		default:
			h.t.Fatalf("Expected GraftError or MultiError but got %T: %v", err, err)
		}
	}
}

// AssertNoError asserts that no error occurred.
func (h *TestHelper) AssertNoError(err error) {
	if err != nil {
		h.t.Fatalf("Unexpected error: %v", err)
	}
}

// MockOperator creates a simple mock operator for testing.
// Implements the Operator interface for use with Engine.RegisterOperator.
type MockOperator struct {
	Name        string
	ReturnValue interface{}
	ReturnError error
	CallCount   int
	LastArgs    []*Expr
	MockPhase   OperatorPhase
}

// Setup implements Operator.Setup.
func (m *MockOperator) Setup() error {
	return nil
}

// Type returns the operator name.
func (m *MockOperator) Type() string {
	return m.Name
}

// Run implements Operator.Run.
func (m *MockOperator) Run(ev *Evaluator, args []*Expr) (*Response, error) {
	m.CallCount++
	m.LastArgs = args

	if m.ReturnError != nil {
		return nil, m.ReturnError
	}

	return &Response{
		Type:  Replace,
		Value: m.ReturnValue,
	}, nil
}

// Dependencies implements Operator.Dependencies.
func (m *MockOperator) Dependencies(_ *Evaluator, _ []*Expr, _ []*tree.Cursor, auto []*tree.Cursor) []*tree.Cursor {
	return auto
}

// Phase implements Operator.Phase.
func (m *MockOperator) Phase() OperatorPhase {
	return m.MockPhase
}

// TestWithMockOperator registers a mock operator for the duration of testFunc,
// then cleans it up.
func (h *TestHelper) TestWithMockOperator(name string, mock *MockOperator, testFunc func()) {
	err := h.engine.RegisterOperator(name, mock)
	if err != nil {
		h.t.Fatalf("Failed to register mock operator %q: %v", name, err)
	}
	defer func() {
		unregErr := h.engine.UnregisterOperator(name)
		if unregErr != nil {
			h.t.Errorf("Failed to unregister mock operator %q: %v", name, unregErr)
		}
	}()

	testFunc()
}

// CompareDocuments compares two documents and returns detailed differences.
func (h *TestHelper) CompareDocuments(doc1, doc2 Document) []string {
	var differences []string

	yaml1, err1 := doc1.ToYAML()
	yaml2, err2 := doc2.ToYAML()

	if err1 != nil || err2 != nil {
		differences = append(differences, fmt.Sprintf("YAML conversion error: doc1=%v, doc2=%v", err1, err2))
		return differences
	}

	if !bytes.Equal(yaml1, yaml2) {
		differences = append(differences, "Documents differ")
		differences = append(differences, fmt.Sprintf("Doc1:\n%s", string(yaml1)))
		differences = append(differences, fmt.Sprintf("Doc2:\n%s", string(yaml2)))
	}

	return differences
}

// AssertDocumentsEqual asserts that two documents are equal.
func (h *TestHelper) AssertDocumentsEqual(doc1, doc2 Document) {
	differences := h.CompareDocuments(doc1, doc2)
	if len(differences) > 0 {
		h.t.Fatalf("Documents are not equal:\n%s", fmt.Sprintf("%v", differences))
	}
}

// CreateTestDocument creates a document from a map for testing.
func (h *TestHelper) CreateTestDocument(data map[string]interface{}) Document {
	return &document{data: data}
}
