// Package boshapi provides types, interfaces, and helpers for working with the
// BOSH Director API.
//
// # Overview
//
// The boshapi package defines the domain types (e.g., Deployment, Task, Release,
// Stemcell) and the interfaces for resource-oriented clients (e.g., DeploymentsClient,
// TasksClient). A concrete implementation of these clients is provided by the
// boshclient package, which wires configuration, transport, authentication, and
// API endpoint discovery. Most consumers should import boshclient to construct a
// client and then interact with the resource client interfaces exposed here.
//
// Getting a client
//
//	import (
//	  "context"
//	  "log"
//
//	  "github.com/fivetwenty-io/boshapi/pkg/boshapi"
//	  "github.com/fivetwenty-io/boshapi/pkg/boshclient"
//	)
//
//	func example() {
//	  ctx := context.Background()
//	  cli, err := boshclient.New(ctx, &boshapi.Config{DirectorURL: "https://bosh.example.com:25555"})
//	  if err != nil { log.Fatal(err) }
//
//	  info, err := cli.GetInfo(ctx)
//	  if err != nil { log.Fatal(err) }
//	  _ = info
//	}
//
// # Async Operations
//
// Most mutating BOSH operations are asynchronous and return a Task. Use the
// TasksClient.PollUntilComplete method to wait for a task to finish:
//
//	task, err := cli.Deployments().Deploy(ctx, manifest, nil)
//	if err != nil { log.Fatal(err) }
//
//	completed, err := cli.Tasks().PollUntilComplete(ctx, task.ID, nil)
//	if err != nil { log.Fatal(err) }
//	_ = completed
//
// # Errors
//
// API errors are represented by APIError. Helpers such as IsNotFound and
// IsUnauthorized make it easy to branch on common error cases.
package boshapi
