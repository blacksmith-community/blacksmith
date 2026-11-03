/*
Package graft provides advanced YAML/JSON document processing capabilities with
sophisticated merging, templating, and transformation features.

# Overview

Graft enables powerful document manipulation through:
  - Advanced document merging with configurable strategies
  - Built-in operator system for data transformation
  - External service integration (Vault, AWS Parameter Store/Secrets Manager)
  - Type-safe document access with structured error handling
  - Comprehensive testing utilities for library users

# Quick Start

The primary entry point is the Engine interface, which provides all
document processing capabilities:

	engine, err := graft.NewEngine()
	if err != nil {
		log.Fatal(err)
	}

	// Parse documents
	doc, err := engine.ParseYAML(yamlData)
	if err != nil {
		log.Fatal(err)
	}

	// Access values with type safety
	name, err := doc.GetString("app.name")
	port, err := doc.GetInt("server.port")
	enabled, err := doc.GetBool("features.logging")

# Document Merging

Merge multiple documents with the builder pattern:

	ctx := context.Background()
	result, err := engine.Merge(ctx, base, override).
		WithPrune("secrets").
		Execute()

# Operator System

Graft includes powerful operators for data transformation:

	// YAML with operators
	config := `
	meta:
	  app_name: "myapp"
	  version: "1.0"

	name: (( concat meta.app_name "-" meta.version ))
	replicas: (( calc meta.environment == "prod" ? 3 : 1 ))
	password: (( vault "secret/myapp:password" ))
	`

	doc, err := engine.ParseYAML([]byte(config))
	result, err := engine.Evaluate(ctx, doc)

# Built-in Operators

  - grab: Reference other document values
  - concat: String concatenation
  - calc: Mathematical and logical calculations
  - vault: Retrieve secrets from HashiCorp Vault
  - awsparam: Get values from AWS Parameter Store
  - awssecret: Get secrets from AWS Secrets Manager
  - static_ips: Generate static IP addresses
  - base64: Base64 encoding/decoding
  - file: Read file contents
  - empty: Check for empty values
  - keys: Get map keys
  - sort: Sort arrays
  - ips: IP address manipulation

# Error Handling

Graft provides structured error types for precise error handling:

	result, err := engine.Evaluate(ctx, doc)
	if err != nil {
		if graftErr, ok := err.(*GraftError); ok {
			switch graftErr.Type {
			case ParseError:
				// Handle YAML/JSON parsing errors
			case EvaluationError:
				// Handle operator evaluation errors
			case ExternalError:
				// Handle Vault/AWS service errors
			}
		}
	}

# Configuration

Configure the engine with functional options:

	engine, err := NewEngine(
		WithConcurrency(10),
		WithCache(true, 1000),
		WithVaultConfig("https://vault.example.com", "token"),
		WithAWSRegion("us-west-2"),
		WithDebugLogging(true),
	)

# Testing

Graft provides comprehensive testing utilities:

	func TestConfig(t *testing.T) {
		helper := NewTestHelper(t)

		config := helper.ParseYAMLString(`
		app:
		  name: "test"
		  port: 8080
		`)

		helper.AssertPathString(config, "app.name", "test")
		helper.AssertPathInt(config, "app.port", 8080)

		// Test merging and evaluation
		result := helper.MustMergeAndEvaluate(base, override)
		helper.AssertPathString(result, "computed.value", "expected")
	}

# External Integrations

## Vault Integration

	engine, err := NewEngine(
		WithVaultConfig("https://vault.company.com", vaultToken),
	)

	// Use in documents
	config := `
	database:
	  password: (( vault "secret/app:db_password" ))
	  url: (( vault "secret/app:db_url" ))
	`

## AWS Integration

	engine, err := NewEngine(
		WithAWSRegion("us-west-2"),
	)

	// Use in documents
	config := `
	app:
	  region: (( awsparam "myapp/region" ))
	  api_key: (( awssecret "myapp/secrets:api_key" ))
	`

# Performance

For high-performance scenarios:

	engine, err := NewEngine(
		WithConcurrency(runtime.NumCPU()),
		WithCache(true, 10000),
		WithMetrics(true),
	)

	// Use context for cancellation
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := engine.Merge(ctx, docs...).Execute()

# Migration from CLI

Common CLI patterns and their library equivalents:

	# CLI
	graft merge base.yml override.yml --prune secrets

	# Library
	result, err := engine.MergeFiles(ctx, "base.yml", "override.yml").
		WithPrune("secrets").
		Execute()

For complete examples and advanced usage patterns, see the examples directory
and the comprehensive README.md documentation.
*/
package graft
