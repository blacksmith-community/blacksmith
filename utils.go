package main

import (
	"fmt"
	"os"
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

func deinterface(o interface{}) interface{} {
	switch o := o.(type) {
	case map[interface{}]interface{}:
		return deinterfaceMap(o)
	case []interface{}:
		return deinterfaceList(o)
	default:
		return o
	}
}

func deinterfaceMap(o map[interface{}]interface{}) map[string]interface{} {
	m := map[string]interface{}{}
	for k, v := range o {
		m[fmt.Sprintf("%v", k)] = deinterface(v)
	}
	return m
}

func deinterfaceList(o []interface{}) []interface{} {
	l := make([]interface{}, len(o))
	for i, v := range o {
		l[i] = deinterface(v)
	}
	return l
}

func readFile(path string) ([]byte, bool, error) {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return []byte{}, false, nil
		}
		return []byte{}, true, err
	}

	b, err := os.ReadFile(path)
	return b, true, err
}

func mergeFiles(required string, optional ...string) (map[interface{}]interface{}, error) {
	// Required Manifest
	b, err := os.ReadFile(required)
	if err != nil {
		return nil, err
	}
	m := make(map[interface{}]interface{})
	err = yaml.Unmarshal(b, &m)
	if err != nil {
		return nil, err
	}

	// Optional Manifests
	for _, path := range optional {
		b, exists, err := readFile(path)
		if !exists {
			continue
		}
		if err != nil {
			return nil, err
		}
		tmp := make(map[interface{}]interface{})
		err = yaml.Unmarshal(b, &tmp)
		if err != nil {
			return nil, err
		}
		m, err = spruce.Merge(m, tmp)
		if err != nil {
			return nil, err
		}
	}

	return m, nil
}
