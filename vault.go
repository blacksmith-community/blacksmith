package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"strconv"
	"strings"

	"github.com/pivotal-golang/lager"
)

type Vault struct {
	URL      string
	Token    string
	Insecure bool
	HTTP     *http.Client
	logger   lager.Logger
}

func (vault *Vault) NewRequest(method, url string, data interface{}) (*http.Request, error) {
	if data == nil {
		return http.NewRequest(method, url, nil)
	}
	cooked, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}

	return http.NewRequest(method, url, strings.NewReader(string(cooked)))
}

func (vault *Vault) Do(method, url string, data interface{}) (*http.Response, error) {
	req, err := vault.NewRequest(method, fmt.Sprintf("%s%s", vault.URL, url), data)
	if err != nil {
		return nil, err
	}

	req.Header.Add("X-Vault-Token", vault.Token)
	return vault.HTTP.Do(req)
}

func (vault *Vault) Clear(instanceID string) {
	var rm func(string)
	rm = func(path string) {
		logger := vault.logger.Session("vault-clear", lager.Data{
			"instance_id": instanceID,
			"path":        path,
		})
		res, err := vault.Do("DELETE", path, nil)
		if err != nil {
			logger.Error("failed-to-delete-secret", err)
		}

		res, err = vault.Do("GET", fmt.Sprintf("%s?list=1", path), nil)
		if err != nil {
			logger.Error("failed-to-list-secret", err)
			return
		}

		b, err := ioutil.ReadAll(res.Body)
		if err != nil {
			logger.Error("failed-to-list-secret", err)
			return
		}

		var r struct{ Data struct{ Keys []string } }
		if err = json.Unmarshal(b, &r); err != nil {
			logger.Error("failed-to-list-secret", err)
			return
		}

		for _, sub := range r.Data.Keys {
			rm(fmt.Sprintf("%s/%s", path, strings.TrimSuffix(sub, "/")))
		}
		logger.Info("cleared-secrets")
	}
	rm(fmt.Sprintf("/v1/secret/%s", instanceID))
}

func (vault *Vault) Track(instanceID, action string, taskID int, credentials interface{}) error {
	task := struct {
		Action      string      `json:"action"`
		Task        int         `json:"task"`
		Credentials interface{} `json:"credentials"`
	}{action, taskID, credentials}
	vault.logger.Debug("vault-track", lager.Data{
		"action":      action,
		"task_id":     taskID,
		"credentials": credentials,
	})
	res, err := vault.Do("POST", fmt.Sprintf("/v1/secret/%s/task", instanceID), task)
	if err != nil {
		return err
	}
	if res.StatusCode != 200 && res.StatusCode != 204 {
		return fmt.Errorf("API %s", res.Status)
	}

	return nil
}

func (vault *Vault) State(instanceID string) (string, int, map[string]interface{}, error) {
	res, err := vault.Do("GET", fmt.Sprintf("/v1/secret/%s/task", instanceID), nil)
	if err != nil {
		return "", 0, nil, err
	}
	if res.StatusCode != 200 && res.StatusCode != 204 {
		return "", 0, nil, fmt.Errorf("API %s", res.Status)
	}

	b, err := ioutil.ReadAll(res.Body)
	if err != nil {
		return "", 0, nil, err
	}

	var raw map[string]interface{}
	if err = json.Unmarshal(b, &raw); err != nil {
		return "", 0, nil, err
	}

	var typ string
	var id int
	var creds map[string]interface{}

	if rawdata, ok := raw["data"]; ok {
		if data, ok := rawdata.(map[string]interface{}); ok {
			if v, ok := data["task"]; ok {
				id, err = strconv.Atoi(fmt.Sprintf("%v", v))
				if err != nil {
					return "", 0, nil, err
				}
			}
			if v, ok := data["action"]; ok {
				typ = fmt.Sprintf("%v", v)
			}
			if v, ok := data["credentials"]; ok {
				if mapped, ok := v.(map[string]interface{}); ok {
					creds = mapped
				}
			}
		}

		return typ, id, creds, nil
	}
	vault.logger.Debug("vault-state", lager.Data{
		"typ":         typ,
		"id":          id,
		"credentials": creds,
	})
	return "", 0, nil, fmt.Errorf("malformed response from vault")
}
