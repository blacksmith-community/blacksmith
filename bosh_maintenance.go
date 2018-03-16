package main

import (
	"github.com/cloudfoundry-community/gogobosh"
)

// Blacksmith needs some sort of background job (goroutine on a ticker?)
//  that checks the BOSH director and marks service instances whose backing deployment have disappeared as such.
//   Otherwise, we can get into a situation where there are no deployments on the director,
//    but a service is "at the limit" w.r.t. number of service deployments in the Vault index.

// clear out of vault all of the secrets that belong to deployments that don't exist anymore
// make sure that decrements quota counter for vault( we either maintain a count for how many of each service we have,
// or we track it on a per deployment instance.  probably located in one big key in vault)
// check vault for a giant key called the index?

// use this code from services.go to find and mark deployments that no longer exist on the bosh director
// grab the big blob of data from db
// iterate over the blob of data and update the service limit
//
//   for _, s := range db.Data {
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
func serviceWithNoDeploymentCheck(bosh *gogobosh.Client, vault *Vault) {
	l := Logger.Wrap("*")
	l.Info("checking for service instances with no backing deployment")
	//grab all current deployments
	deployments, err := bosh.GetDeployments()
	if err != nil {
		l.Error(err.Error())
	}

	var markedForDeath []gogobosh.Deployment
	vaultDB, err := vault.getVaultDB()
	if err != nil {
		l.Error(err.Error())
	}
	//TODO
	//loop through whats in the bosh director,
	//at each deployment in the director, grab that key from vault, and add it to a list
	//at the end of the loop through the bosh stuff put all of the newly created lists of things
	//into vault using the save function
	for _, deployment := range deployments {
		//deployments are named as instance.PlanID + "-" + instanceID
		var p Plan
		for _, s := range vaultDB.Data {
			if ss, ok := s.(map[string]interface{}); ok {
				service, haveService := ss["service_id"]
				plan, havePlan := ss["plan_id"]
				instance
				if havePlan && haveService {
					if v, ok := service.(string); ok && v == p.Service.ID {
						existingService += 1
						if v, ok := plan.(string); ok && v == p.ID {
							existingPlan += 1
						}
					}
				}
			}
		}

		b.Vault.Index(instanceID, map[string]interface{}{
			"service_id": details.ServiceID,
			"plan_id":    plan.ID,
		})
		//looks like instanceid is the key, with service id and plan id as values
		if !vaultDB.Contains(deployment.Name) {
			markedForDeath = append(markedForDeath, deployment)
		}
	}
}

// We also need another scheduled task (goroutine on a ticker?)
//  to run bosh-cleanup against the BOSH director. This should be configurable,
//   in case someone wants to manage their BOSH director out-of-band,
//and doesn't want Blacksmith "cleaning up" things they were using.
func boshCleanup(bosh *gogobosh.Client) {
	l := Logger.Wrap("*")
	l.Info("running bosh-cleanup against the bosh director")
	//pending the pr to gogobosh
	//bosh.cleanup("all")
}

//bosh deployments are named as follows:
// deploymentName := instance.PlanID + "-" + instanceID
