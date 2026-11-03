# Graft Go Library

Graft is a powerful YAML/JSON document processing library that provides advanced merging, templating, and transformation capabilities. Originally designed as a CLI tool, graft now offers a comprehensive Go library interface for integration into Go applications.

## Features

- 🔀 **Advanced Document Merging**: Merge YAML/JSON documents with sophisticated rules
- 🎯 **Operator System**: Built-in operators for data transformation (`grab`, `concat`, `calc`, etc.)
- 🔧 **Flexible Configuration**: Functional options pattern for engine configuration
- 🧪 **Testing Utilities**: Built-in test helpers for library users
- ⚡ **High Performance**: Optimized for large documents with concurrent processing
- 🛡️ **Type Safety**: Structured error handling with specific error types
- 🔌 **Extensible**: Support for custom operators and integrations

## Quick Start

### Installation

```bash
go get github.com/fivetwenty-io/graft/pkg/graft
```

### Basic Usage

```go
package main

import (
    "context"
    "fmt"
    "log"

    "github.com/fivetwenty-io/graft/pkg/graft"
)

func main() {
    // Create a new engine
    engine, err := graft.NewEngineV2()
    if err != nil {
        log.Fatal(err)
    }

    // Parse YAML documents
    base, err := engine.ParseYAML([]byte(`
name: myapp
config:
  enabled: true
  timeout: 30
`))
    if err != nil {
        log.Fatal(err)
    }

    override, err := engine.ParseYAML([]byte(`
config:
  timeout: 60
  retries: 3
new_field: added
`))
    if err != nil {
        log.Fatal(err)
    }

    // Merge documents
    ctx := context.Background()
    result, err := engine.Merge(ctx, base, override).Execute()
    if err != nil {
        log.Fatal(err)
    }

    // Access merged values
    timeout, err := result.GetInt("config.timeout")
    if err != nil {
        log.Fatal(err)
    }
    fmt.Printf("Timeout: %d\n", timeout) // Output: Timeout: 60

    // Convert back to YAML
    yamlBytes, err := result.ToYAML()
    if err != nil {
        log.Fatal(err)
    }
    fmt.Printf("Result:\n%s", string(yamlBytes))
}
```

## Core Concepts

### Engine

The `EngineV2` is the main entry point for all graft operations. It provides methods for parsing, merging, and evaluating documents.

```go
// Create with default settings
engine, err := graft.NewEngineV2()

// Create with custom options
engine, err := graft.NewEngineV2(
    graft.WithConcurrency(10),
    graft.WithCache(true, 1000),
    graft.WithVaultConfig("https://vault.example.com", "token"),
)
```

### Documents

Documents represent YAML/JSON data with a user-friendly interface:

```go
// Parse documents
doc, err := engine.ParseYAML(yamlData)
doc, err := engine.ParseJSON(jsonData)
doc, err := engine.ParseFile("config.yml")

// Access values with type safety
name, err := doc.GetString("app.name")
count, err := doc.GetInt("config.count")
enabled, err := doc.GetBool("features.enabled")
items, err := doc.GetSlice("data.items")
```

### Merging

Graft provides sophisticated document merging with the builder pattern:

```go
result, err := engine.Merge(ctx, base, override).
    WithPrune("secrets").
    WithCherryPick("config", "app").
    Execute()
```

#### Merge Options

- **WithPrune**: Remove specified keys from the final result
- **WithCherryPick**: Keep only specified keys in the final result
- **SkipEvaluation**: Skip operator processing after merging
- **EnableGoPatch**: Enable go-patch format support
- **FallbackAppend**: Use append mode for arrays instead of replace

### Operators

Graft includes a powerful operator system for data transformation:

```yaml
# Reference other values
name: (( grab meta.app_name ))

# String concatenation
image: (( concat meta.app_name ":" meta.version ))

# Mathematical calculations
replicas: (( calc meta.environment == "prod" ? 3 : 1 ))

# External data sources
password: (( vault "secret/myapp:password" ))
region: (( awsparam "app/region" ))
```

### Error Handling

Graft provides structured error types for better error handling:

```go
result, err := engine.Evaluate(ctx, doc)
if err != nil {
    if graftErr, ok := err.(*graft.GraftError); ok {
        switch graftErr.Type {
        case graft.ParseError:
            // Handle parse errors
        case graft.EvaluationError:
            // Handle evaluation errors
        case graft.OperatorError:
            // Handle operator errors
        }
    }
}
```

## Configuration Options

### Engine Options

```go
engine, err := graft.NewEngineV2(
    // Performance tuning
    graft.WithConcurrency(10),              // Max concurrent operations
    graft.WithCache(true, 1000),            // Enable caching with size limit

    // External integrations
    graft.WithVaultConfig("vault-url", "token"),
    graft.WithAWSRegion("us-west-2"),

    // Debugging
    graft.WithDebugLogging(true),
    graft.WithMetrics(true),
)
```

### Vault Integration

```go
// Configure Vault
engine, err := graft.NewEngineV2(
    graft.WithVaultConfig("https://vault.example.com", "vault-token"),
)

// Use vault operator in documents
doc, err := engine.ParseYAML([]byte(`
database:
  password: (( vault "secret/myapp:password" ))
  connection: (( vault "secret/myapp:connection_string" ))
`))
```

### AWS Integration

```go
// Configure AWS
engine, err := graft.NewEngineV2(
    graft.WithAWSRegion("us-west-2"),
)

// Use AWS operators
doc, err := engine.ParseYAML([]byte(`
config:
  region: (( awsparam "app/region" ))
  api_key: (( awssecret "app/secrets:api_key" ))
`))
```

## Testing

Graft provides comprehensive testing utilities for library users:

```go
func TestMyConfiguration(t *testing.T) {
    helper := graft.NewTestHelper(t)
    
    // Parse test data
    config := helper.ParseYAMLString(`
name: test-app
config:
  enabled: true
  timeout: 30
`)
    
    // Make assertions
    helper.AssertPathString(config, "name", "test-app")
    helper.AssertPathBool(config, "config.enabled", true)
    helper.AssertPathInt(config, "config.timeout", 30)
    
    // Test merging
    override := helper.ParseYAMLString(`
config:
  timeout: 60
`)
    
    result := helper.MustMergeAndEvaluate(config, override)
    helper.AssertPathInt(result, "config.timeout", 60)
}
```

### Test Utilities

- `NewTestHelper(t)`: Create a test helper instance
- `ParseYAMLString(yaml)`: Parse YAML from string
- `MustMerge(docs...)`: Merge documents (fails test on error)
- `MustEvaluate(doc)`: Evaluate document (fails test on error)
- `AssertPath*()`: Type-safe assertions for document values
- `AssertError()`: Structured error type assertions

## Advanced Usage

### Custom Operators

```go
type CustomOperator struct{}

func (op *CustomOperator) Type() string {
    return "custom"
}

func (op *CustomOperator) Run(ev *graft.Evaluator, args []interface{}) (interface{}, error) {
    // Custom operator logic
    return "custom-result", nil
}

// Register custom operator
engine, err := graft.NewEngineV2(
    graft.WithCustomOperator("custom", &CustomOperator{}),
)
```

### Streaming Processing

```go
// Process multiple files
readers := []io.Reader{file1, file2, file3}
result, err := engine.MergeReaders(ctx, readers...).Execute()

// Process with context cancellation
ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
defer cancel()

result, err := engine.Merge(ctx, docs...).Execute()
```

### Performance Optimization

```go
// Configure for large documents
engine, err := graft.NewEngineV2(
    graft.WithConcurrency(runtime.NumCPU()),
    graft.WithCache(true, 10000),
)

// Use benchmarks to test performance
func BenchmarkMerge(b *testing.B) {
    engine, _ := graft.NewEngineV2()
    docs := loadTestDocuments()
    
    b.ResetTimer()
    for i := 0; i < b.N; i++ {
        _, err := engine.Merge(context.Background(), docs...).Execute()
        if err != nil {
            b.Fatal(err)
        }
    }
}
```

## Migration from CLI

If you're migrating from using graft as a CLI tool to using it as a library:

### CLI Command Equivalents

```bash
# CLI: graft merge base.yml override.yml
```

```go
// Library equivalent:
baseDoc, _ := engine.ParseFile("base.yml")
overrideDoc, _ := engine.ParseFile("override.yml")
result, _ := engine.Merge(ctx, baseDoc, overrideDoc).Execute()
```

```bash
# CLI: graft merge --prune secrets base.yml override.yml
```

```go
// Library equivalent:
result, _ := engine.Merge(ctx, baseDoc, overrideDoc).
    WithPrune("secrets").
    Execute()
```

### Common Patterns

| CLI Pattern | Library Equivalent |
|-------------|-------------------|
| `graft merge *.yml` | `engine.MergeFiles(ctx, paths...)` |
| `graft diff a.yml b.yml` | Compare documents with test utilities |
| `graft json input.yml` | `doc.ToJSON()` |
| `graft --prune key` | `.WithPrune("key")` |
| `graft --cherry-pick key` | `.WithCherryPick("key")` |

## Error Reference

### Error Types

- `graft.ParseError`: YAML/JSON parsing errors
- `graft.MergeError`: Document merging errors  
- `graft.EvaluationError`: Operator evaluation errors
- `graft.OperatorError`: Specific operator errors
- `graft.ValidationError`: Input validation errors
- `graft.ConfigurationError`: Engine configuration errors
- `graft.ExternalError`: External service errors (Vault, AWS)

### Common Error Patterns

```go
// Check specific error types
if graftErr, ok := err.(*graft.GraftError); ok {
    fmt.Printf("Error type: %s\n", graftErr.Type)
    fmt.Printf("Message: %s\n", graftErr.Message)
    if graftErr.Path != "" {
        fmt.Printf("Path: %s\n", graftErr.Path)
    }
}

// Handle specific scenarios
switch {
case graft.IsParseError(err):
    // Handle invalid YAML/JSON
case graft.IsOperatorError(err):
    // Handle operator issues
case graft.IsExternalError(err):
    // Handle Vault/AWS connectivity issues
}
```

## Best Practices

### Performance

- Use appropriate concurrency settings for your workload
- Enable caching for repeated operations
- Use context cancellation for long-running operations
- Benchmark your specific use cases

### Error Handling

- Always check for specific error types
- Use structured logging with error context
- Implement retry logic for external service errors
- Validate input documents before processing

### Testing

- Use the provided test utilities
- Test both success and error cases
- Use property-based testing for complex merging scenarios
- Benchmark performance-critical paths

### Security

- Validate all external inputs
- Use secure Vault/AWS configurations
- Avoid logging sensitive data from documents
- Implement proper access controls for external services

## Examples

See the `examples/` directory for complete working examples:

- [Basic merging](examples/basic/)
- [Operator usage](examples/operators/)
- [Vault integration](examples/vault/)
- [AWS integration](examples/aws/)
- [Testing patterns](examples/testing/)
- [Performance optimization](examples/performance/)

## Contributing

Contributions are welcome! Please see the [Contributing Guide](CONTRIBUTING.md) for details.

## License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.