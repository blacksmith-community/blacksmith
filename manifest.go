package main

import (
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

func GenManifest(p Plan, params map[interface{}]interface{}) (string, error) {
	var manifest map[interface{}]interface{}
	err := yaml.Unmarshal([]byte(p.RawManifest), &manifest)
	if err != nil {
		return "", err
	}

	final, err := spruce.Merge(manifest, wrap("meta.params", params))
	if err != nil {
		return "", err
	}

	b, err := yaml.Marshal(final)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
