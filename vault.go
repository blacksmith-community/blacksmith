package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
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

type VaultCreds struct {
	SealKey   string `json:"seal_key"`
	RootToken string `json:"root_token"`
}

func (vault *Vault) Init(store string) error {
	logger := vault.logger.Session("vault-init", lager.Data{})

	res, err := vault.Do("GET", "/v1/sys/init", nil)
	if err != nil {
		logger.Error("failed-to-check-vault", err)
		return err
	}

	b, err := ioutil.ReadAll(res.Body)
	if err != nil {
		logger.Error("failed-to-read-init-response", err)
		return err
	}

	var init struct {
		Initialized bool `json:"initialized"`
	}
	if err = json.Unmarshal(b, &init); err != nil {
		logger.Error("failed-to-parse-init-response", err)
		return err
	}
	if init.Initialized {
		b, err := ioutil.ReadFile(store)
		if err != nil {
			logger.Error("failed-to-read-vault-credentials-file", err)
			return err
		}
		creds := VaultCreds{}
		err = json.Unmarshal(b, &creds)
		if err != nil {
			logger.Error("failed-to-parse-vault-credentials-file", err)
			return err
		}
		vault.Token = creds.RootToken
		os.Setenv("VAULT_TOKEN", vault.Token)
		return vault.Unseal(creds.SealKey)
	}

	//////////////////////////////////////////

	res, err = vault.Do("PUT", "/v1/sys/init", map[string]int{
		"secret_shares":    1,
		"secret_threshold": 1,
	})
	if err != nil {
		logger.Error("failed-to-init-vault", err)
		return err
	}

	b, err = ioutil.ReadAll(res.Body)
	if err != nil {
		logger.Error("failed-to-read-init-response", err)
		return err
	}

	var keys struct {
		RootToken string   `json:"root_token"`
		Keys      []string `json:"keys"`
	}
	if err = json.Unmarshal(b, &keys); err != nil {
		logger.Error("failed-to-parse-init-response", err)
		return err
	}
	if keys.RootToken == "" || len(keys.Keys) != 1 {
		err = fmt.Errorf("invalid response from vault: token '%s' and %d keys", keys.RootToken, len(keys.Keys))
		logger.Error("failed-to-parse-init-response", err)
		return err
	}

	creds := VaultCreds{
		SealKey:   keys.Keys[0],
		RootToken: keys.RootToken,
	}
	b, err = json.Marshal(creds)
	if err != nil {
		logger.Error("failed-to-marshal-vault-credentials", err)
		return err
	}
	err = ioutil.WriteFile(store, b, 0600)
	if err != nil {
		logger.Error("failed-to-write-vault-credentials-file", err)
		return err
	}

	vault.Token = creds.RootToken
	os.Setenv("VAULT_TOKEN", vault.Token)
	return vault.Unseal(creds.SealKey)
}

func (vault *Vault) Unseal(key string) error {
	logger := vault.logger.Session("vault-unseal", lager.Data{})

	res, err := vault.Do("GET", "/v1/sys/seal-status", nil)
	if err != nil {
		logger.Error("failed-to-query-seal-status", err)
		return err
	}

	b, err := ioutil.ReadAll(res.Body)
	if err != nil {
		logger.Error("failed-to-read-seal-status-response-body", err)
		return err
	}

	var status struct {
		Sealed bool `json:"sealed"`
	}
	err = json.Unmarshal(b, &status)
	if err != nil {
		logger.Error("failed-to-parse-seal-status-response-body", err)
		return err
	}

	if !status.Sealed {
		return nil
	}

	//////////////////////////////////////////

	res, err = vault.Do("POST", "/v1/sys/unseal", map[string]string{
		"key": key,
	})
	if err != nil {
		logger.Error("failed-to-unseal-vault", err)
		return err
	}

	b, err = ioutil.ReadAll(res.Body)
	if err != nil {
		logger.Error("failed-to-read-unseal-response-body", err)
		return err
	}
	err = json.Unmarshal(b, &status)
	if err != nil {
		logger.Error("failed-to-parse-unseal-response-body", err)
		return err
	}

	if status.Sealed {
		err = fmt.Errorf("vault is still sealed after unseal attempt")
		logger.Error("failed-to-unseal-vault", err)
		return err
	}

	return nil
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

func (vault *Vault) Get(path string) (map[string]interface{}, bool, error) {
	exists := false

	res, err := vault.Do("GET", fmt.Sprintf("/v1/secret/%s", path), nil)
	if err != nil {
		return nil, exists, err
	}
	if res.StatusCode == 404 {
		return nil, exists, nil
	}
	if res.StatusCode != 200 && res.StatusCode != 204 {
		return nil, exists, fmt.Errorf("API %s", res.Status)
	}

	exists = true
	b, err := ioutil.ReadAll(res.Body)
	if err != nil {
		return nil, exists, err
	}

	var raw map[string]interface{}
	if err = json.Unmarshal(b, &raw); err != nil {
		return nil, exists, err
	}

	if x, ok := raw["data"]; ok {
		return x.(map[string]interface{}), exists, nil
	}

	return nil, exists, fmt.Errorf("Malformed response from Vault")
}

func (vault *Vault) Put(path string, data interface{}) error {
	res, err := vault.Do("POST", fmt.Sprintf("/v1/secret/%s", path), data)
	if err != nil {
		return err
	}
	if res.StatusCode != 200 && res.StatusCode != 204 {
		return fmt.Errorf("API %s", res.Status)
	}
	return nil
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

func (vault *Vault) Track(instanceID, action string, taskID int, params interface{}) error {
	task := struct {
		Action string      `json:"action"`
		Task   int         `json:"task"`
		Params interface{} `json:"params"`
	}{action, taskID, deinterface(params)}
	vault.logger.Debug("vault-track", lager.Data{
		"action":  action,
		"task_id": taskID,
	})

	return vault.Put(fmt.Sprintf("%s/task", instanceID), task)
}

func (vault *Vault) Index(instanceID string, data interface{}) error {
	idx, err := vault.GetIndex("db")
	if err != nil {
		return err
	}
	if data != nil {
		idx.Data[instanceID] = data
	} else {
		delete(idx.Data, instanceID)
	}
	return idx.Save()
}

func (vault *Vault) State(instanceID string) (string, int, map[string]interface{}, error) {
	data, exists, err := vault.Get(fmt.Sprintf("%s/task", instanceID))
	if err != nil {
		return "", 0, nil, err
	}
	if !exists {
		return "", 0, nil, fmt.Errorf("Instance %s not found in Vault", instanceID)
	}

	var typ string
	var id int
	var params map[string]interface{}

	if v, ok := data["task"]; ok {
		id, err = strconv.Atoi(fmt.Sprintf("%v", v))
		if err != nil {
			return "", 0, nil, err
		}
	}
	if v, ok := data["action"]; ok {
		typ = fmt.Sprintf("%v", v)
	}
	if v, ok := data["params"]; ok {
		if mapped, ok := v.(map[string]interface{}); ok {
			params = mapped
		}
	}

	return typ, id, params, nil
}
