package graft

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/fivetwenty-io/graft/pkg/graft/tree"
)

// This file contains examples of how to use the graft library
// These are not tests, but documentation examples

// ExampleBasicMerge demonstrates basic document merging.
func ExampleBasicMerge() {
	// Create an engine with default settings
	engine, err := CreateDefaultEngine()
	if err != nil {
		log.Fatal(err)
	}

	// Parse YAML documents
	doc1, _ := engine.ParseYAML([]byte(`
name: myapp
version: 1.0
config:
  database: postgres
`))

	doc2, _ := engine.ParseYAML([]byte(`
config:
  cache: redis
  timeout: 30s
deployment:
  replicas: 3
`))

	// Merge documents
	result, err := engine.Merge(context.Background(), doc1, doc2).Execute()
	if err != nil {
		log.Fatal(err)
	}

	// Convert back to YAML
	output, _ := engine.ToYAML(result)
	fmt.Println(string(output))
}

// ExampleWithOperators demonstrates using graft operators.
func ExampleWithOperators() {
	engine, _ := CreateDefaultEngine()

	doc, _ := engine.ParseYAML([]byte(`
database:
  host: (( concat "db-" environment ))
  port: 5432
  name: (( grab meta.database.name ))
  
meta:
  database:
    name: myapp_prod
    
environment: production
`))

	// Evaluate operators
	result, err := engine.Evaluate(context.Background(), doc)
	if err != nil {
		log.Fatal(err)
	}

	output, _ := engine.ToYAML(result)
	fmt.Println(string(output))
	// Output:
	// database:
	//   host: db-production
	//   port: 5432
	//   name: myapp_prod
	// environment: production
	// meta:
	//   database:
	//     name: myapp_prod
}

// ExampleWithPruning demonstrates selective key removal.
func ExampleWithPruning() {
	engine, _ := CreateDefaultEngine()

	doc1, _ := engine.ParseYAML([]byte(`
app:
  name: myapp
  version: 1.0
secrets:
  api_key: secret123
  db_password: secret456
`))

	doc2, _ := engine.ParseYAML([]byte(`
app:
  environment: production
secrets:
  jwt_secret: secret789
`))

	// Merge and prune secrets
	result, err := engine.Merge(context.Background(), doc1, doc2).
		WithPrune("secrets").
		Execute()
	if err != nil {
		log.Fatal(err)
	}

	output, _ := engine.ToYAML(result)
	fmt.Println(string(output))
	// Output:
	// app:
	//   name: myapp
	//   version: 1.0
	//   environment: production
}

// ExampleWithCherryPick demonstrates selective key inclusion.
func ExampleWithCherryPick() {
	engine, _ := CreateDefaultEngine()

	doc, _ := engine.ParseYAML([]byte(`
app:
  name: myapp
  version: 1.0
  environment: production
secrets:
  api_key: secret123
deployment:
  replicas: 3
  strategy: rolling
metadata:
  created_by: admin
  created_at: 2023-01-01
`))

	// Only keep app and deployment sections
	result, err := engine.Merge(context.Background(), doc).
		WithCherryPick("app", "deployment").
		Execute()
	if err != nil {
		log.Fatal(err)
	}

	output, _ := engine.ToYAML(result)
	fmt.Println(string(output))
}

// ExampleCustomOperator demonstrates registering a custom operator.
func ExampleCustomOperator() {
	engine, _ := CreateDefaultEngine()

	// Register a custom operator
	customOp := &CustomUppercaseOperator{}
	err := engine.RegisterOperator("uppercase", customOp)
	if err != nil {
		log.Fatal(err)
	}

	doc, _ := engine.ParseYAML([]byte(`
app:
  name: (( uppercase "myapp" ))
  title: (( uppercase "My Application" ))
`))

	result, err := engine.Evaluate(context.Background(), doc)
	if err != nil {
		log.Fatal(err)
	}

	output, _ := engine.ToYAML(result)
	fmt.Println(string(output))
}

// ExampleDocumentManipulation demonstrates the Document interface.
func ExampleDocumentManipulation() {
	engine, _ := CreateDefaultEngine()

	doc, _ := engine.ParseYAML([]byte(`
app:
  name: myapp
  config:
    database:
      host: localhost
      port: 5432
`))

	// Get values using paths
	appName, _ := doc.Get("app.name")
	fmt.Printf("App name: %s\n", appName)

	dbHost, _ := doc.Get("app.config.database.host")
	fmt.Printf("DB host: %s\n", dbHost)

	// Set values
	_ = doc.Set("app.version", "2.0")
	_ = doc.Set("app.config.database.ssl", true)

	// Get all top-level keys
	keys := doc.Keys()
	fmt.Printf("Top-level keys: %v\n", keys)

	// Convert back to YAML
	output, _ := engine.ToYAML(doc)
	fmt.Println(string(output))
}

// ExampleConfiguredEngine demonstrates creating an engine with custom configuration.
func ExampleConfiguredEngine() {
	// Create engine with custom configuration
	engine, err := NewEngine(
		WithLogger(&CustomLogger{}),
		WithCache(true, 2000),
		WithConcurrency(20),
		WithMetrics(true),
		WithAWSConfig(&AWSConfig{
			Region:  "us-west-2",
			Profile: "production",
		}),
	)
	if err != nil {
		log.Fatal(err)
	}

	// Use the configured engine
	doc, _ := engine.ParseYAML([]byte(`
database:
  host: (( awsparam "/myapp/prod/db/host" ))
  password: (( awssecret "myapp/prod/db:password" ))
`))

	result, err := engine.Evaluate(context.Background(), doc)
	if err != nil {
		log.Fatal(err)
	}

	output, _ := engine.ToYAML(result)
	fmt.Println(string(output))
}

// CustomUppercaseOperator is an example custom operator.
type CustomUppercaseOperator struct{}

// Setup initializes the operator.
func (o *CustomUppercaseOperator) Setup() error { return nil }

// Phase returns the operator phase.
func (o *CustomUppercaseOperator) Phase() OperatorPhase { return EvalPhase }

// Dependencies returns the operator dependencies.
func (o *CustomUppercaseOperator) Dependencies(ev *Evaluator, args []*Expr, locs, auto []*tree.Cursor) []*tree.Cursor {
	return nil
}

// Run executes the uppercase operator.
func (o *CustomUppercaseOperator) Run(ev *Evaluator, args []*Expr) (*Response, error) {
	if len(args) != 1 {
		return nil, NewOperatorError("uppercase", "requires exactly one argument", nil)
	}

	value := args[0].String()
	return &Response{
		Type:  Replace,
		Value: strings.ToUpper(value),
	}, nil
}

// CustomLogger is an example logger implementation.
type CustomLogger struct{}

// Debug logs a debug message.
func (l *CustomLogger) Debug(msg string, fields ...interface{}) {
	log.Printf("[DEBUG] "+msg, fields...)
}

// Info logs an info message.
func (l *CustomLogger) Info(msg string, fields ...interface{}) {
	log.Printf("[INFO] "+msg, fields...)
}

// Warn logs a warning message.
func (l *CustomLogger) Warn(msg string, fields ...interface{}) {
	log.Printf("[WARN] "+msg, fields...)
}

func (l *CustomLogger) Error(msg string, fields ...interface{}) {
	log.Printf("[ERROR] "+msg, fields...)
}
