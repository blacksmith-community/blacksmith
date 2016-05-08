package services

import (
	"io/ioutil"
	"log"
	"os"
)

// GetService get a service based on path
func GetService(path string) (service Service) {
	folders := []os.FileInfo{}
	files, err := ioutil.ReadDir(path)
	if err != nil {
		log.Fatal(err)
	}
	info, err := ioutil.ReadFile(path + "/info.yml")
	if err != nil {
		log.Printf("error reading manifest.yml for plan %v", err)
	}
	service.Info = string(info)
	for _, file := range files {
		if file.IsDir() {
			folders = append(folders, file)
			plan := GetPlan(path + "/" + file.Name())
			service.Plans = append(service.Plans, plan)
		}
	}
	return
}

//GetPlan based on given path
func GetPlan(path string) (plan Plan) {
	manifest, err := ioutil.ReadFile(path + "/manifest.yml")
	if err != nil {
		log.Printf("error reading manifest.yml for plan %v", err)
	}
	info, err := ioutil.ReadFile(path + "/info.yml")
	if err != nil {
		log.Printf("error reading plan.yml for plan %v", err)
	}
	plan.Manifest = string(manifest)
	plan.Info = string(info)
	return
}
