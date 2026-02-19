// Package osbapi provides shared domain types, interfaces, and errors for the
// Open Service Broker API v2.17 specification.
//
// This package defines the core types used by both the client (platform) and
// server (broker) implementations:
//
//   - Catalog types: Service, Plan, Schemas, DashboardClient
//   - Request/response types for all 10 OSB API operations
//   - The ServiceBroker interface for broker authors
//   - The Client interface for platform integrations
//   - OSBError and sentinel errors for spec-compliant error handling
//   - Header constants and originating identity encoding
//
// For creating a broker, see the [github.com/fivetwenty-io/osbapi/v2/pkg/broker] package.
// For creating a client, see the [github.com/fivetwenty-io/osbapi/v2/pkg/osbclient] package.
package osbapi
