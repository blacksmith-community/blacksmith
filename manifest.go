package main

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/cloudfoundry-community/gogobosh"
	"github.com/geofffranks/spruce"
	"gopkg.in/yaml.v2"
)

func InitManifest(p Plan, instanceID string) error {
	os.Chmod(p.InitScriptPath, 755)
	cmd := exec.Command(p.InitScriptPath)

	cmd.Env = os.Environ()
	cmd.Env = append(cmd.Env, fmt.Sprintf("CREDENTIALS=secret/%s", instanceID))
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
