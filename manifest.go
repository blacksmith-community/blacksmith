package main

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/geofffranks/spruce"
	"gopkg.in/yaml.v2"
)

func wrap(key string, data map[interface{}]interface{}) map[interface{}]interface{} {
	kk := strings.Split(key, ".")

	for i := 0; i < len(kk)/2; i++ {
		s := kk[i]
		kk[i] = kk[len(kk)-i-1]
		kk[len(kk)-i-1] = s
	}
	for _, k := range kk {
		data = map[interface{}]interface{}{k: data}
	}
	return data
}

func InitManifest(p Plan, instanceID string, params map[interface{}]interface{}) error {
	cmd := exec.Command(p.InitScript)

	cmd.Env = append(cmd.Env, fmt.Sprintf("CREDENTIALS=secret/%s", instanceID))
	/* put more environment variables here, as needed */

	_, err := cmd.CombinedOutput()
	//	logger.Debug("initialized-manifest", lager.Data{
	//		"output": string(out),
	//	})
	return err
}

func GenManifest(p Plan, params map[interface{}]interface{}) (string, map[string]interface{}, error) {
	var manifest map[interface{}]interface{}
	credentials := make(map[string]interface{})

	err := yaml.Unmarshal([]byte(p.RawManifest), &manifest)
	if err != nil {
		return "", credentials, err
	}

	merged, err := spruce.Merge(manifest, wrap("meta.params", params))
	if err != nil {
		return "", credentials, err
	}
	eval := &spruce.Evaluator{Tree: merged}
	err = eval.Run([]string{})
	if err != nil {
		return "", credentials, err
	}
	final := eval.Tree

	if m, ok := final["meta"]; ok {
		if mm, ok := m.(map[interface{}]interface{}); ok {
			if s, ok := mm["service"]; ok {
				if ss, ok := s.(map[interface{}]interface{}); ok {
					for k, v := range ss {
						credentials[k.(string)] = v
					}
				}
			}
		}
	}

	b, err := yaml.Marshal(final)
	if err != nil {
		return "", credentials, err
	}
	return string(b), credentials, nil
}
