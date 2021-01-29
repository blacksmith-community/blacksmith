package main

import (
	"fmt"
	"io/ioutil"
	"regexp"

	"github.com/pivotal-cf/brokerapi"
	"gopkg.in/yaml.v2"
)

type Plan struct {
	ID          string `yaml:"id" json:"id"`
	Name        string `yaml:"name" json:"name"`
	Description string `yaml:"description" json:"description"`
	Limit       int    `yaml:"limit" json:"limit"`

	Manifest          map[interface{}]interface{} `json:"-"`
	Credentials       map[interface{}]interface{} `json:"-"`
	InitScriptPath    string                      `json:"-"`
	UpgradeScriptPath string                      `json:"-"`

	Service *Service `yaml:"service" json:"service"`
}

type Service struct {
	ID          string   `yaml:"id" json:"id"`
	Name        string   `yaml:"name" json:"name"`
	Description string   `yaml:"description" json:"description"`
	Bindable    bool     `yaml:"bindable" json:"bindable"`
	Tags        []string `yaml:"tags" json:"tags"`
	Limit       int      `yaml:"limit" json:"limit"`
	Plans       []Plan   `yaml:"plans" json:"plans"`
}

var ValidName *regexp.Regexp

func init() {
	ValidName = regexp.MustCompile("^[a-zA-Z0-9][a-zA-Z0-9_.-]*$")
}

func CheckNames(names ...string) error {
	for _, s := range names {
		if !ValidName.MatchString(s) {
			return fmt.Errorf("'%s' is invalid; names should only contain letters, numbers and hyphens.", s)
		}
	}
	return nil
}

func (p Plan) String() string {
	return fmt.Sprintf("%s/%s", p.Service.ID, p.ID)
}

func (p Plan) OverLimit(db *VaultIndex) bool {
	if p.Limit == 0 && p.Service.Limit == 0 {
		return false
	}

	existingPlan := 0
	existingService := 0

	for _, s := range db.Data {
		if ss, ok := s.(map[string]interface{}); ok {
			service, haveService := ss["service_id"]
			plan, havePlan := ss["plan_id"]
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

	if p.Limit > 0 && existingPlan >= p.Limit {
		return true
	}

	if p.Service.Limit > 0 && existingService >= p.Service.Limit {
		return true
	}

	return false
}

func ReadPlan(path string) (p Plan, err error) {
	b, err := ioutil.ReadFile(fmt.Sprintf("%s/plan.yml", path))
	if err != nil {
		return
	}
	err = yaml.Unmarshal(b, &p)
	if err != nil {
		return
	}
	if p.ID == "" && p.Name != "" {
		p.ID = p.Name
	}
	if p.Name == "" && p.ID != "" {
		p.Name = p.ID
	}
	if err = CheckNames(p.ID, p.Name); err != nil {
		return
	}

	p.Manifest, err = mergeFiles(
		fmt.Sprintf("%s/manifest.yml", path),
		fmt.Sprintf("%s/params.yml", path),
	)
	if err != nil {
		return
	}

	b, exists, err := readFile(fmt.Sprintf("%s/credentials.yml", path))
	if exists {
		if err != nil {
			return
		}
		err = yaml.Unmarshal(b, &p.Credentials)
		if err != nil {
			return
		}
	} else if err != nil {
		return
	} else {
		p.Credentials = make(map[interface{}]interface{})
	}

	p.InitScriptPath = fmt.Sprintf("%s/init", path)
	p.UpgradeScriptPath = fmt.Sprintf("%s/upgrade", path)
	return
}

func ReadPlans(dir string, service Service) ([]Plan, error) {
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
			p.ID = service.ID + "-" + p.ID
			p.Service = &service
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
		return s, fmt.Errorf("%s: %s", file, err)
	}

	err = yaml.Unmarshal(b, &s)
	if err != nil {
		return s, fmt.Errorf("%s: %s", file, err)
	}
	if s.ID == "" && s.Name != "" {
		s.ID = s.Name
	}
	if s.Name == "" && s.ID != "" {
		s.Name = s.ID
	}
	if err = CheckNames(s.ID, s.Name); err != nil {
		return s, fmt.Errorf("%s: %s", file, err)
	}

	pp, err := ReadPlans(path, s)
	if err != nil {
		return s, fmt.Errorf("%s: %s", file, err)
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
			return nil, fmt.Errorf("%s: %s", dir, err)
		}

		for _, f := range ls {
			if f.IsDir() {
				s, err := ReadService(fmt.Sprintf("%s/%s", dir, f.Name()))
				if err != nil {
					return nil, fmt.Errorf("%s/%s: %s", dir, f.Name(), err)
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
		var md brokerapi.ServiceMetadata
		bb[i].ID = s.ID
		bb[i].Name = s.Name
		bb[i].Description = s.Description
		bb[i].Bindable = s.Bindable
		bb[i].Tags = make([]string, len(s.Tags))
		bb[i].Metadata = &md
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
