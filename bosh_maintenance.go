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

	//turn deployments from a slice of deployments into a map of
	//string to bool because all we care about is the name of the deployment (the string here)
	var deploymentNames map[string]bool
	for _, deployment := range deployments {
		deploymentNames[deployment.Name] = true
	}
	//grab the vault DB json blob out of vault
	vaultDB, err := vault.getVaultDB()
	if err != nil {
		l.Error(err.Error())
	}

	//loop through all current instances in the "db"
	//check bosh director for each instance in the "db"
	//if the instance matches to one in the director delete it from the list
	//the list you're left with will be all of the instances that need to be deleted
	//delete those instances by calling the index function with the instance id and nil (deletes them)
	for _, serviceInstance := range vaultDB.Data {
		if ss, ok := serviceInstance.(map[string]interface{}); ok {
			service, _ := ss["service_id"]
			plan, _ := ss["plan_id"]

			//deployments are named as instance.PlanID + "-" + instanceID
			if val, ok := deploymentNames[plan.PlanID+"-"+serviceInstance.ID]; !ok {
				//if the deployment name isn't listed in our director then delete it from vault
				//passing a nil data value to vault index will delete it from vault and then save for us
				vault.Index(serviceInstance.ID, nil)
			}

		}
		l.Error("could not find ")

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
