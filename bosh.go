package main

import (
	"fmt"

	"github.com/cloudfoundry-community/gogobosh"
	"gopkg.in/yaml.v2"
)

func GetStemcellBlock(BOSH *gogobosh.Client, deploymentName string, useLatest bool) (map[interface{}]interface{}, error) {
	stemcells := make(map[interface{}]interface{})

	l := Logger.Wrap("Get Stemcell Block")

	deployments, err := BOSH.GetDeployments()
	if err != nil {
		return nil, err
	}
	l.Info("Found deployments: %s", deployments)

	stemcellYAML := ""
	currentStemcellName := ""
	currentStemcellVersion := ""

	for _, deployment := range deployments {
		if deployment.Name == deploymentName {
			// We must have a stemcell listed or somethings wrong with the deployment and we should bail
			if len(deployment.Stemcells) == 0 {
				return nil, fmt.Errorf("Missing stemcells in %s", deployment)
			}
			if len(deployment.Stemcells) > 1 {
				return nil, fmt.Errorf("too many stemcells in %s", deployment)
			}

			// Grab newest stemcell from list
			currentStemcellName = deployment.Stemcells[len(deployment.Stemcells)-1].Name
			currentStemcellVersion = deployment.Stemcells[len(deployment.Stemcells)-1].Version

			version := ""
			// TODO: detect stemcell type
			if useLatest {
				version = "latest"
			} else {
				version = currentStemcellVersion
			}
			stemcellYAML = fmt.Sprintf("---\nstemcells:\n- alias: default\n  os: ubuntu-xenial\n  version: \"%s\"\n", version)
		}
	}

	l.Debug("Deployment %s uses Stemcell %s (%s)", deploymentName, currentStemcellName, currentStemcellVersion)

	if stemcellYAML == "" {
		return nil, fmt.Errorf("%s not found looking for stemcells", deploymentName)
	}

	err = yaml.Unmarshal([]byte(stemcellYAML), &stemcells)

	return stemcells, err
}

func GetReleasesBlock(BOSH *gogobosh.Client, deploymentName string, useLatest bool) (map[interface{}]interface{}, error) {
	releases := make(map[interface{}]interface{})

	l := Logger.Wrap("Get Release Block")

	deployments, err := BOSH.GetDeployments()
	if err != nil {
		return nil, err
	}
	//	l.Info("Found deployments: %s", deployments)

	releaseYAML := ""

	for _, deployment := range deployments {
		if deployment.Name == deploymentName {
			// We must have a stemcell listed or somethings wrong with the deployment and we should bail
			if len(deployment.Releases) == 0 {
				return nil, fmt.Errorf("Missing releases in %s", deployments)
			}
			releaseYAML = "---\nreleases:\n"

			for _, release := range deployment.Releases {
				l.Debug("Found release %s (%s)", release.Name, release.Version)

				version := ""
				if useLatest {
					version = "latest"
				} else {
					version = release.Version
				}
				releaseYAML += fmt.Sprintf("- name: \"%s\"\n  version: \"%s\"\n", release.Name, version)
			}
		}
	}

	l.Debug("Releases Block %s", releaseYAML)

	if releaseYAML == "" {
		return nil, fmt.Errorf("%s not found looking for releases", deploymentName)
	}

	err = yaml.Unmarshal([]byte(releaseYAML), &releases)

	return releases, err
}
