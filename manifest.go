package main

import (
	"fmt"
	"os"
	"os/exec"

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
	err = eval.Run([]string{})
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
