package main

import (
	"fmt"
)

// Blacksmith needs some sort of background job (goroutine on a ticker?)
//  that checks the BOSH director and marks service instances whose backing deployment have disappeared as such.
//   Otherwise, we can get into a situation where there are no deployments on the director,
//    but a service is "at the limit" w.r.t. number of service deployments in the Vault index.
func serviceWithNoDeploymentCheck() {
	fmt.Print("checking for service instances with no backing deployment")
}

// We also need another scheduled task (goroutine on a ticker?)
//  to run bosh-cleanup against the BOSH director. This should be configurable,
//   in case someone wants to manage their BOSH director out-of-band,
//and doesn't want Blacksmith "cleaning up" things they were using.
func boshCleanup() {
	fmt.Print("running bosh-cleanup against the bosh director")
}
