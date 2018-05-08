package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"strconv"
	"strings"
)

type Vault struct {
	URL      string
	Token    string
	Insecure bool
	HTTP     *http.Client
}

type VaultCreds struct {
	SealKey   string `json:"seal_key"`
	RootToken string `json:"root_token"`
}

func (vault *Vault) Init(store string) error {
	l := Logger.Wrap("vault init")

	l.Debug("checking initialization state of the vault")
	res, err := vault.Do("GET", "/v1/sys/init", nil)
	if err != nil {
		l.Error("failed to check initialization state of the vault: %s", err)
		return err
	}
	b, err := ioutil.ReadAll(res.Body)
	if err != nil {
		l.Error("failed to read response from the vault, concerning its initialization state: %s", err)
		return err
	}
	var init struct {
		Initialized bool `json:"initialized"`
	}
	if err = json.Unmarshal(b, &init); err != nil {
		l.Error("failed to parse response from the vault, concerning its initialization state: %s", err)
		return err
	}
	if init.Initialized {
		l.Info("vault is already initialized")

		l.Debug("reading credentials files from %s", store)
		b, err := ioutil.ReadFile(store)
		if err != nil {
			l.Error("failed to read vault credentials from %s: %s", store, err)
			return err
		}
		creds := VaultCreds{}
		err = json.Unmarshal(b, &creds)
		if err != nil {
			l.Error("failed to parse vault credentials from %s: %s", store, err)
			return err
		}
		vault.Token = creds.RootToken
		os.Setenv("VAULT_TOKEN", vault.Token)
		return vault.Unseal(creds.SealKey)
	}

	//////////////////////////////////////////

	l.Info("initializing the vault with 1/1 keys")
	res, err = vault.Do("PUT", "/v1/sys/init", map[string]int{
		"secret_shares":    1,
		"secret_threshold": 1,
	})
	if err != nil {
		l.Error("failed to initialize the vault: %s", err)
		return err
	}
	b, err = ioutil.ReadAll(res.Body)
	if err != nil {
		l.Error("failed to read response from the vault, concerning our initialization attempt: %s", err)
		return err
	}

	var keys struct {
		RootToken string   `json:"root_token"`
		Keys      []string `json:"keys"`
	}
	if err = json.Unmarshal(b, &keys); err != nil {
		l.Error("failed to parse response from the vault, concerning our initialization attempt: %s", err)
		return err
	}
	if keys.RootToken == "" || len(keys.Keys) != 1 {
		if keys.RootToken == "" {
			l.Error("failed to initialize vault: root token was blank")
		}
		if len(keys.Keys) != 1 {
			l.Error("failed to initialize vault: incorrect number of seal keys (%d) returned", len(keys.Keys))
		}
		err = fmt.Errorf("invalid response from vault: token '%s' and %d keys", keys.RootToken, len(keys.Keys))
		return err
	}

	creds := VaultCreds{
		SealKey:   keys.Keys[0],
		RootToken: keys.RootToken,
	}
	l.Debug("marshaling credentials for longterm storage")
	b, err = json.Marshal(creds)
	if err != nil {
		l.Error("failed to marshal vault root token / seal key for longterm storage: %s", err)
		return err
	}
	l.Debug("storing credentials at %s (mode 0600)", store)
	err = ioutil.WriteFile(store, b, 0600)
	if err != nil {
		l.Error("failed to write credentials to longterm storage file %s: %s", store, err)
		return err
	}

	vault.Token = creds.RootToken
	os.Setenv("VAULT_TOKEN", vault.Token)
	return vault.Unseal(creds.SealKey)
}

func (vault *Vault) Unseal(key string) error {
	l := Logger.Wrap("vault unseal")

	l.Debug("checking current seal status of the vault")
	res, err := vault.Do("GET", "/v1/sys/seal-status", nil)
	if err != nil {
		l.Error("failed to check current seal status of the vault: %s", err)
		return err
	}
	b, err := ioutil.ReadAll(res.Body)
	if err != nil {
		l.Error("failed to read response from the vault, concerning current seal status: %s", err)
		return err
	}

	var status struct {
		Sealed bool `json:"sealed"`
	}
	err = json.Unmarshal(b, &status)
	if err != nil {
		l.Error("failed to parse response from the vault, concerning current seal status: %s", err)
		return err
	}

	if !status.Sealed {
		l.Info("vault is already unsealed")
		return nil
	}

	//////////////////////////////////////////

	l.Info("vault is sealed; unsealing it")
	res, err = vault.Do("POST", "/v1/sys/unseal", map[string]string{
		"key": key,
	})
	if err != nil {
		l.Error("failed to unseal vault: %s", err)
		return err
	}
	b, err = ioutil.ReadAll(res.Body)
	if err != nil {
		l.Error("failed to read response from the vault, concerning our unseal attempt: %s", err)
		return err
	}
	err = json.Unmarshal(b, &status)
	if err != nil {
		l.Error("failed to parse response from the vault, concerning our unseal attempt: %s", err)
		return err
	}

	if status.Sealed {
		err = fmt.Errorf("vault is still sealed after unseal attempt")
		l.Error("%s", err)
		return err
	}

	l.Info("unsealed the vault")
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
	l := Logger.Wrap("vault clear %s", instanceID)

	var rm func(string)
	rm = func(path string) {
		l.Debug("removing Vault secrets at/below %s", path)

		res, err := vault.Do("DELETE", path, nil)
		if err != nil {
			l.Error("failed to delete %s: %s", path, err)
		}

		res, err = vault.Do("GET", fmt.Sprintf("%s?list=1", path), nil)
		if err != nil {
			l.Error("failed to list secrets at %s: %s", path, err)
			return
		}
		b, err := ioutil.ReadAll(res.Body)
		if err != nil {
			l.Error("failed to read response from the vault: %s", err)
			return
		}

		var r struct{ Data struct{ Keys []string } }
		if err = json.Unmarshal(b, &r); err != nil {
			l.Error("failed to parse response from the vault: %s", err)
			return
		}

		for _, sub := range r.Data.Keys {
			rm(fmt.Sprintf("%s/%s", path, strings.TrimSuffix(sub, "/")))
		}

		l.Debug("cleared out vault secrets")
	}
	l.Info("removing secrets under /v1/secrets/%s", instanceID)
	rm(fmt.Sprintf("/v1/secret/%s", instanceID))
	l.Info("completed")
}

func (vault *Vault) Track(instanceID, action string, taskID int, params interface{}) error {
	l := Logger.Wrap("vault track %s", instanceID)
	l.Debug("tracking action '%s', task %d", action, taskID)

	task := struct {
		Action string      `json:"action"`
		Task   int         `json:"task"`
		Params interface{} `json:"params"`
	}{action, taskID, deinterface(params)}

	return vault.Put(fmt.Sprintf("%s/task", instanceID), task)
}

func (vault *Vault) Index(instanceID string, data interface{}) error {
	idx, err := vault.GetIndex("db")
	//TODO replace with list of uuids?
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

type Instance struct {
	ID        string
	ServiceID string
	PlanID    string
}

func (vault *Vault) FindInstance(id string) (*Instance, bool, error) {
	idx, err := vault.GetIndex("db")
	if err != nil {
		return nil, false, err
	}

	raw, err := idx.Lookup(id)
	if err != nil {
		return nil, false, nil /* not found */
	}

	inst, ok := raw.(map[string]interface{})
	if !ok {
		return nil, true, fmt.Errorf("indexed value [%s] is malformed (not a real map)", id)
	}

	instance := &Instance{}
	if v, ok := inst["service_id"]; ok {
		if s, ok := v.(string); ok {
			instance.ServiceID = s
		}
	}
	if v, ok := inst["plan_id"]; ok {
		if s, ok := v.(string); ok {
			instance.PlanID = s
		}
	}
	return instance, true, nil
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

// getVaultDB returns the vault index (some useful vault constructs) that we're using to keep track of service/plan usage data
func (vault *Vault) getVaultDB() (*VaultIndex, error) {
	l := Logger.Wrap("task.log")
	l.Debug("retrieving vault 'db' index (for tracking service usage)")
	db, err := vault.GetIndex("db")
	if err != nil {
		l.Error("failed to get 'db' index out of the vault: %s", err)
		return nil, err
	}
	return db, nil
}

// db:map[
//     34c43057-1136-4008-bf18-1746c4bfaabd:map[
//         plan_id:postgresql-standalone
//         service_id:postgresql
//     ]

//     49274b83-9ce9-4e6d-932e-a5d732c9fe87:map[
//         plan_id:redis-dedicated-cache
//         service_id:redis
//     ]

//     7f7b83cd-95ac-4c21-95f6-1d0fe567e952:map[
//         plan_id:rabbitmq-dedicated
//         service_id:rabbitmq
//     ]
// ]
