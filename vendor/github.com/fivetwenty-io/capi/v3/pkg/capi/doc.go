// Package capi provides types, interfaces, and helpers for working with the
// Cloud Foundry V3 API.
//
// # Overview
//
// The capi package defines the domain types (e.g., App, Space, Route, Package,
// Droplet) and the interfaces for resource-oriented clients (e.g., AppsClient,
// SpacesClient). A concrete implementation of these clients is provided by the
// cfclient package, which wires configuration, transport, authentication, and
// API link discovery. Most consumers should import cfclient to construct a
// client and then interact with the resource client interfaces exposed here.
//
// Getting a client
//
//	import (
//	  "context"
//	  "log"
//
//	  "github.com/fivetwenty-io/capi/v3/pkg/capi"
//	  "github.com/fivetwenty-io/capi/v3/pkg/cfclient"
//	)
//
//	func example() {
//	  ctx := context.Background()
//	  cli, err := cfclient.New(&capi.Config{APIEndpoint: "https://api.example.com"})
//	  if err != nil { log.Fatal(err) }
//
//	  // List the first page of apps
//	  apps, err := cli.Apps().List(ctx, capi.NewQueryParams().WithPerPage(50))
//	  if err != nil { log.Fatal(err) }
//	  _ = apps
//	}
//
// # Queries and pagination
//
// Use QueryParams to express common list options (page, per_page, order_by,
// include, filters). The package also provides helpers for iterating or
// collecting paginated results:
//
//	it := capi.NewPaginationIterator(ctx, cli.Apps(), "/v3/apps", capi.NewQueryParams())
//	for it.HasNext() {
//	  app, err := it.Next()
//	  if err != nil { break }
//	  _ = app
//	}
//
// or fetch all results at once:
//
//	all, err := capi.FetchAllPages(ctx, cli.Apps(), "/v3/apps", nil, capi.DefaultPaginationOptions())
//	if err != nil { /* handle error */ }
//	_ = all
//
// # Errors
//
// API errors are represented by APIError and ResponseError. Helpers such as
// IsNotFound, IsUnauthorized, and IsForbidden make it easy to branch on common
// CF error cases.
//
// # Interceptors and caching
//
// The package includes generic building blocks such as request/response
// interceptors (for logging, auth headers, metrics, rate limiting, circuit
// breaking) and a simple pluggable Cache abstraction. The cfclient package
// composes these pieces for a sensible default client; applications with
// advanced needs can also use these primitives directly.
//
// # Resources
//
// Resource clients follow a consistent CRUD-and-actions pattern across CF
// resources (Apps, Organizations, Spaces, Routes, Packages, Droplets, Builds,
// Deployments, Processes, Tasks, Stacks, Users, Roles, SecurityGroups,
// IsolationSegments, FeatureFlags, Jobs, OrganizationQuotas, SpaceQuotas,
// Sidecars, Revisions, Env Var Groups, Events, etc.). See the individual
// interfaces in resource_clients.go for the full surface area.
package capi
