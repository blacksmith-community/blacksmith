package main

// Blacksmith needs some sort of background job (goroutine on a ticker?)
//  that checks the BOSH director and marks service instances whose backing deployment have disappeared as such.
//   Otherwise, we can get into a situation where there are no deployments on the director,
//    but a service is "at the limit" w.r.t. number of service deployments in the Vault index.

//clear out of vault all of the secrets that belong to deployments that don't exist anymore
//make sure that decrements quota counter for vault( we either maintain a count for how many of each service we have,
// or we track it on a per deployment instance.  probably located in one big key in vault)
//check vault for a giant key called the index?

// use this code from services.go to find and mark deployments that no longer exist on the bosh director
//  for _, s := range db.Data {
// 		if ss, ok := s.(map[string]interface{}); ok {
// 			service, haveService := ss["service_id"]
// 			plan, havePlan := ss["plan_id"]
// 			if havePlan && haveService {
// 				if v, ok := service.(string); ok && v == p.Service.ID {
// 					existingService += 1
// 					if v, ok := plan.(string); ok && v == p.ID {
// 						existingPlan += 1
// 					}
// 				}
// 			}
// 		}
// 	}
func serviceWithNoDeploymentCheck() {
	l := Logger.Wrap("*")
	l.Info("checking for service instances with no backing deployment")
}

// We also need another scheduled task (goroutine on a ticker?)
//  to run bosh-cleanup against the BOSH director. This should be configurable,
//   in case someone wants to manage their BOSH director out-of-band,
//and doesn't want Blacksmith "cleaning up" things they were using.
func boshCleanup() {
	l := Logger.Wrap("*")
	l.Info("running bosh-cleanup against the bosh director")
}
