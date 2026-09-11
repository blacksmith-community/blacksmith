// Package cfclient provides the primary entry point for constructing a
// Cloud Foundry V3 API client that implements the capi.Client interface.
//
// It layers configuration, HTTP transport, authentication, and API root/links
// discovery on top of the resource interfaces and types defined in the capi
// package. Most applications should import cfclient to build a client, then use
// the returned capi.Client to access resource-specific clients, for example
// Apps(), Spaces(), Routes(), etc.
//
// Quick start
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
//
//	  // Minimal: just an API endpoint (no auth).
//	  cli, err := cfclient.New(&capi.Config{APIEndpoint: "https://api.example.com"})
//	  if err != nil { log.Fatal(err) }
//
//	  // Or with an access token you already have:
//	  cli, err = cfclient.New(&capi.Config{
//	    APIEndpoint: "https://api.example.com",
//	    AccessToken: "eyJhbGciOi...", // bearer token
//	  })
//
//	  // Or with username/password or client credentials. When credentials are
//	  // provided and no token URL is set, cfclient discovers the UAA endpoint
//	  // from the CF root (/) and sets TokenURL automatically.
//	  cli, err = cfclient.New(&capi.Config{
//	    APIEndpoint:  "https://api.example.com",
//	    Username:     "user",
//	    Password:     "pass",
//	    // alternatively:
//	    // ClientID:     "client-id",
//	    // ClientSecret: "client-secret",
//	  })
//	  if err != nil { log.Fatal(err) }
//
//	  // Use resource clients via the capi.Client interface
//	  apps, err := cli.Apps().List(ctx, capi.NewQueryParams().WithPerPage(10))
//	  if err != nil { log.Fatal(err) }
//	  _ = apps
//	}
//
// # TLS and development mode
//
// For local development, you can set Config.SkipTLSVerify=true. This is gated by
// the environment variable CAPI_DEV_MODE to avoid accidental insecure usage in
// production environments.
//
// # Helpers
//
// The package also provides convenience constructors NewWithEndpoint,
// NewWithToken, NewWithClientCredentials, and NewWithPassword that wrap New
// with the appropriate configuration.
package cfclient
