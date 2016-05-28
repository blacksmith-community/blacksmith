package main

import (
	"fmt"
	"io/ioutil"

	"github.com/pivotal-cf/brokerapi"
	"gopkg.in/yaml.v2"
)

type Plan struct {
	ID          string `yaml:"id"`
	Name        string `yaml:"name"`
	Description string `yaml:"description"`

	RawManifest string
}

type Service struct {
	ID          string   `yaml:"id"`
	Name        string   `yaml:"name"`
	Description string   `yaml:"description"`
	Bindable    bool     `yaml:"bindable"`
	Tags        []string `yaml:"tags"`

	Plans []Plan
}

func ReadPlan(path string) (p Plan, err error) {
	file := fmt.Sprintf("%s/plan.yml", path)

	b, err := ioutil.ReadFile(file)
	if err != nil {
		return
	}

	err = yaml.Unmarshal(b, &p)
	raw := fmt.Sprintf("%s/manifest.yml", path)
	b, err = ioutil.ReadFile(raw)
	if err != nil {
		return
	}
	p.RawManifest = string(b)
	return
}

func ReadPlans(dir string) ([]Plan, error) {
	pp := make([]Plan, 0)

	ls, err := ioutil.ReadDir(dir)
	if err != nil {
		return pp, err
	}

	for _, f := range ls {
		if f.IsDir() {
			p, err := ReadPlan(fmt.Sprintf("%s/%s", dir, f.Name()))
			if err != nil {
				return pp, err
			}
			pp = append(pp, p)
		}
	}
	return pp, err
}

func ReadService(path string) (Service, error) {
	var s Service
	file := fmt.Sprintf("%s/service.yml", path)

	b, err := ioutil.ReadFile(file)
	if err != nil {
		return s, err
	}

	err = yaml.Unmarshal(b, &s)
	if err != nil {
		return s, err
	}

	pp, err := ReadPlans(path)
	if err != nil {
		return s, err
	}
	s.Plans = pp
	return s, nil
}

func ReadServices(dirs ...string) ([]Service, error) {
	if len(dirs) == 0 {
		return nil, fmt.Errorf("no service directories found")
	}

	ss := make([]Service, 0)
	for _, dir := range dirs {
		ls, err := ioutil.ReadDir(dir)
		if err != nil {
			return nil, err
		}

		for _, f := range ls {
			if f.IsDir() {
				s, err := ReadService(fmt.Sprintf("%s/%s", dir, f.Name()))
				if err != nil {
					return nil, err
				}
				ss = append(ss, s)
			}
		}
	}
	return ss, nil
}

func Catalog(ss []Service) []brokerapi.Service {
	bb := make([]brokerapi.Service, len(ss))
	for i, s := range ss {
		bb[i].ID = s.ID
		bb[i].Name = s.Name
		bb[i].Description = s.Description
		bb[i].Bindable = s.Bindable
		copy(bb[i].Tags, s.Tags)
		bb[i].Plans = make([]brokerapi.ServicePlan, len(s.Plans))
		for j, p := range s.Plans {
			bb[i].Plans[j].ID = p.ID
			bb[i].Plans[j].Name = p.Name
			bb[i].Plans[j].Description = p.Description
			/* FIXME: support free */
		}
	}

	return bb
}
