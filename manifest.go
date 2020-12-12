package main

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/cloudfoundry-community/gogobosh"
	"github.com/geofffranks/spruce"
	"github.com/smallfish/simpleyaml"
	"gopkg.in/yaml.v2"
)

const (
	// If BLACKSMITH_INSTANCE_DATA_DIR environment variable is not set, use this as a default
	DefaultBlacksmithWorkDir = "/var/vcap/data/blacksmith/"
)

func GetWorkDir() string {
	var blacksmithWorkDir = os.Getenv("BLACKSMITH_INSTANCE_DATA_DIR")
	if blacksmithWorkDir == "" {
		blacksmithWorkDir = DefaultBlacksmithWorkDir
	}
	return blacksmithWorkDir
}

func InitManifest(p Plan, instanceID string) error {
	/* skip running the plan initialization script if it doesn't exist */
	if _, err := os.Stat(p.InitScriptPath); err != nil && os.IsNotExist(err) {
		return nil
	}

	/* otherwise, execute it (chmodding to cut Forge authors some slack...) */
	os.Chmod(p.InitScriptPath, 0755)
	cmd := exec.Command(p.InitScriptPath)

	cmd.Env = os.Environ()
	cmd.Env = append(cmd.Env, fmt.Sprintf("CREDENTIALS=secret/%s", instanceID))
	cmd.Env = append(cmd.Env, fmt.Sprintf("RAWJSONFILE=%s%s.json", GetWorkDir(), instanceID))
	cmd.Env = append(cmd.Env, fmt.Sprintf("YAMLFILE=%s%s.yml", GetWorkDir(), instanceID))
	cmd.Env = append(cmd.Env, fmt.Sprintf("BLACKSMITH_INSTANCE_DATA_DIR=%s", GetWorkDir()))
	cmd.Env = append(cmd.Env, fmt.Sprintf("INSTANCE_ID=%s", instanceID))
	cmd.Env = append(cmd.Env, fmt.Sprintf("BLACKSMITH_PLAN=%s", p.ID))
	/* put more environment variables here, as needed */

	out, err := cmd.CombinedOutput()
	Debug("init script `%s' said:\n%s", p.InitScriptPath, string(out))
	return err
}

func GenManifest(p Plan, manifests ...map[interface{}]interface{}) (string, error) {
	merged, err := spruce.Merge(p.Manifest)
	if err != nil {
		return "", err
	}
	for _, next := range manifests {
		merged, err = spruce.Merge(merged, next)
		if err != nil {
			return "", err
		}
	}
	eval := &spruce.Evaluator{Tree: merged}
	err = eval.Run(nil, nil)
	if err != nil {
		return "", err
	}
	final := eval.Tree

	b, err := yaml.Marshal(final)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func UpgradePrep(p Plan, instanceID string, oldManifest string) (bool, error) {
	/* Bail from regenerating manifest if plan upgrade script if it doesn't exist */
	if p.UpgradeScriptPath == "" {
		return false, nil
	}
	if _, err := os.Stat(p.UpgradeScriptPath); err != nil && os.IsNotExist(err) {
		return false, nil
	}

	/* otherwise, execute it (chmodding to cut Forge authors some slack...) */
	os.Chmod(p.UpgradeScriptPath, 0755)
	cmd := exec.Command(p.UpgradeScriptPath)

	cmd.Env = os.Environ()
	cmd.Env = append(cmd.Env, fmt.Sprintf("CREDENTIALS=secret/%s", instanceID))
	cmd.Env = append(cmd.Env, fmt.Sprintf("RAWJSONFILE=%s%s.json", GetWorkDir(), instanceID))
	cmd.Env = append(cmd.Env, fmt.Sprintf("YAMLFILE=%s%s.yml", GetWorkDir(), instanceID))
	cmd.Env = append(cmd.Env, fmt.Sprintf("BLACKSMITH_INSTANCE_DATA_DIR=%s", GetWorkDir()))
	cmd.Env = append(cmd.Env, fmt.Sprintf("INSTANCE_ID=%s", instanceID))
	cmd.Env = append(cmd.Env, fmt.Sprintf("BLACKSMITH_PLAN=%s", p.ID))
	cmd.Env = append(cmd.Env, fmt.Sprintf("CURRENT_MANIFEST=%s", oldManifest))
	/* put more environment variables here, as needed */

	out, err := cmd.CombinedOutput()
	Debug("upgrade script `%s' said:\n%s", p.UpgradeScriptPath, string(out))
	return (err == nil), err
}

func UploadReleasesFromManifest(raw string, bosh *gogobosh.Client, l *Log) error {
	var manifest struct {
		Releases []struct {
			Name    string `yaml:"name"`
			Version string `yaml:"version"`
			URL     string `yaml:"url"`
			SHA1    string `yaml:"sha1"`
		} `yaml:"releases"`
	}

	err := yaml.Unmarshal([]byte(raw), &manifest)
	if err != nil {
		return err
	}

	l.Debug("enumerating uploaded BOSH releases")
	rr, err := bosh.GetReleases()
	if err != nil {
		return err
	}

	have := make(map[string]bool)
	for _, rl := range rr {
		for _, v := range rl.ReleaseVersions {
			l.Debug("found BOSH release %s/%s", rl.Name, v.Version)
			have[rl.Name+"/"+v.Version] = true
		}
	}

	l.Debug("determining which BOSH releases need uploaded")
	for _, rl := range manifest.Releases {
		if have[rl.Name+"/"+rl.Version] {
			l.Debug("already have %s/%s; skipping upload", rl.Name, rl.Version)
		} else if rl.URL == "" {
			l.Debug("%s/%s is missing either its URL; skipping upload", rl.Name, rl.Version)
		} else if rl.SHA1 == "" {
			l.Debug("%s/%s is missing either its SHA1 checksum; skipping upload", rl.Name, rl.Version)
		} else {
			l.Debug("uploading BOSH release %s/%s from %s (sha1 %s)...", rl.Name, rl.Version, rl.URL, rl.SHA1)
			_, err := bosh.UploadRelease(rl.URL, rl.SHA1)
			if err != nil {
				return err
			}
		}
	}

	return nil
}

func GetCreds(id string, plan Plan, bosh *gogobosh.Client, l *Log) (interface{}, error) {
	var jobs []*Job
	jobsYAML := make(map[string][]*Job)

	deployment := plan.ID + "-" + id
	l.Debug("looking up BOSH VM information for %s", deployment)
	vms, err := bosh.GetDeploymentVMs(deployment)
	if err != nil {
		l.Error("failed to retrieve BOSH VM information for %s: %s", deployment, err)
		return nil, err
	}

	os.Setenv("CREDENTIALS", fmt.Sprintf("secret/%s", id))
	byType := make(map[string]*Job)
	for _, vm := range vms {
		job := Job{vm.JobName + "/" + strconv.Itoa(vm.Index), vm.IPs}
		l.Debug("found job %s with IPs [%s]", job.Name, strings.Join(vm.IPs, ", "))
		jobs = append(jobs, &job)

		if typ, ok := byType[vm.JobName]; ok {
			for _, ip := range vm.IPs {
				typ.IPs = append(typ.IPs, ip)
			}
		} else {
			byType[vm.JobName] = &Job{vm.JobName, vm.IPs}
		}
	}
	for _, job := range byType {
		jobs = append(jobs, job)
	}
	jobsYAML["jobs"] = jobs
	l.Debug("marshaling BOSH VM information")
	jobsMarshal, err := yaml.Marshal(jobsYAML)
	if err != nil {
		l.Error("failed to marshal BOSH VM information (for credentials.yml merge): %s", err)
		return nil, err
	}
	l.Debug("converting BOSH VM information to YAML")
	yamlJobs, err := simpleyaml.NewYaml(jobsMarshal)
	if err != nil {
		l.Error("failed to convert BOSH VM information to YAML (for credentials.yml merge): %s", err)
		return nil, err
	}
	jobsIfc, err := yamlJobs.Map()
	l.Debug("parsing BOSH VM information from YAML (don't ask)")
	if err != nil {
		l.Error("failed to parse BOSH VM information from YAML (for credentials.yml merge): %s", err)
		return nil, err
	}

	l.Debug("merging service deployment manifest with credentials.yml (for retrieve/bind)")
	manifest, err := GenManifest(plan, jobsIfc, plan.Credentials)
	if err != nil {
		l.Error("failed to merge service deployment manifest with credentials.yml: %s", err)
		return nil, err
	}

	l.Debug("parsing merged YAML super-structure, to retrieve `credentials' top-level key")
	yamlManifest, err := simpleyaml.NewYaml([]byte(manifest))
	if err != nil {
		l.Error("failed to parse merged YAML; unable to retrieve credentials for bind: %s", err)
		return nil, err
	}

	l.Debug("retrieving `credentials' top-level key, to return to the caller")
	yamlCreds := yamlManifest.Get("credentials")
	yamlMap, err := yamlCreds.Map()
	if err != nil {
		l.Error("failed to retrieve `credentials' top-level key: %s", err)
		return nil, err
	}

	return deinterfaceMap(yamlMap), nil
}
