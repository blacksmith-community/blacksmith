package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
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
	defer func() {
		ioutil.ReadAll(res.Body)
		res.Body.Close()
	}()
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
		vault.updateHomeDirs()
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
	defer func() {
		ioutil.ReadAll(res.Body)
		res.Body.Close()
	}()
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
	vault.updateHomeDirs()
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
	defer func() {
		ioutil.ReadAll(res.Body)
		res.Body.Close()
	}()
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
	defer func() {
		ioutil.ReadAll(res.Body)
		res.Body.Close()
	}()
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

func (vault *Vault) EnsureVaultV2() error {
	l := Logger.Wrap("vault KV version")

	options := make(map[interface{}]interface{})
	options["version"] = "2"

	l.Info("Ensuring KV is version 2")
	_, err := vault.Do("POST", "/v1/sys/mounts/secret/tune", wrap("options", options))
	// TODO: check result
	return err
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

func (vault *Vault) Get(path string, out interface{}) (bool, error) {
	exists := false

	res, err := vault.Do("GET", fmt.Sprintf("/v1/secret/data/%s", path), nil)
	if err != nil {
		return exists, err
	}
	defer func() {
		ioutil.ReadAll(res.Body)
		res.Body.Close()
	}()
	if res.StatusCode == 404 {
		return exists, nil
	}
	if res.StatusCode != 200 && res.StatusCode != 204 {
		return exists, fmt.Errorf("API %s", res.Status)
	}

	exists = true
	b, err := ioutil.ReadAll(res.Body)
	if err != nil {
		return exists, err
	}

	if out == nil {
		return exists, nil
	}

	var data_wrapper struct {
		Data map[string]interface{} `json:"data"`
	}

	//	var raw map[string]interface{}
	if err = json.Unmarshal(b, &data_wrapper); err != nil {
		return exists, err
	}

	var data interface{}
	var ok bool
	if data, ok = data_wrapper.Data["data"]; !ok {
		return exists, fmt.Errorf("Malformed response from Vault")
	}

	dataBytes, err := json.Marshal(&data)
	if err != nil {
		return exists, fmt.Errorf("could not remarshal vault data")
	}

	err = json.Unmarshal(dataBytes, &out)
	return exists, err
}

func (vault *Vault) Put(path string, data interface{}) error {
	res, err := vault.Do("POST", fmt.Sprintf("/v1/secret/data/%s", path), map[string]interface{}{
		"data": data,
	})
	if err != nil {
		return err
	}
	defer func() {
		ioutil.ReadAll(res.Body)
		res.Body.Close()
	}()
	if res.StatusCode != 200 && res.StatusCode != 204 {
		return fmt.Errorf("API %s", res.Status)
	}
	return nil
}

func (vault *Vault) Delete(path string) error {
	res, err := vault.Do("DELETE", fmt.Sprintf("/v1/secret/data/%s", path), nil)
	if err != nil {
		return err
	}
	defer func() {
		ioutil.ReadAll(res.Body)
		res.Body.Close()
	}()

	if res.StatusCode != 200 && res.StatusCode != 204 && res.StatusCode != 404 {
		return fmt.Errorf("API %s", res.Status)
	}
	return nil
}

func (vault *Vault) Clear(instanceID string) {
	l := Logger.Wrap("vault clear %s", instanceID)

	var rm func(string)
	rm = func(path string) {
		l.Debug("removing Vault secrets at/below %s", path)

		if err := vault.Delete(path); err != nil {
			l.Error("failed to delete %s: %s", path, err)
		}

		res, err := vault.Do("GET", fmt.Sprintf("%s?list=1", path), nil)
		if err != nil {
			l.Error("failed to list secrets at %s: %s", path, err)
			return
		}
		defer func() {
			ioutil.ReadAll(res.Body)
			res.Body.Close()
		}()
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
	if err != nil {
		return err
	}
	if data != nil {
		idx.Data[instanceID] = data
		return idx.Save()
	}

	delete(idx.Data, instanceID)
	err = idx.Save()
	vault.Clear(instanceID)
	return err
}

func (vault *Vault) StoreState(instanceID string, manifest map[interface{}]interface{}, credentials map[interface{}]interface{}, params map[interface{}]interface{}, initScriptPath string, upgradeScriptPath string) error {

	l := Logger.Wrap("vault store %s", instanceID)
	l.Debug("Storing files for plan")

	initFile, err := ioutil.ReadFile(initScriptPath)
	if err != nil {
		initFile = nil
	}

	upgradeFile, err := ioutil.ReadFile(upgradeScriptPath)
	if err != nil {
		upgradeFile = nil
	}

	state := struct {
		Manifest      interface{} `json:"manifest"`
		Credentials   interface{} `json:"credentials"`
		Params        interface{} `json:"params"`
		InitScript    string      `json:"init"`
		UpgradeScript string      `json:"upgrade"`
	}{deinterface(manifest), deinterface(credentials), deinterface(params), string(initFile), string(upgradeFile)}

	return vault.Put(fmt.Sprintf("%s/state", instanceID), state)
}

func (vault *Vault) RestoreState(instanceID string) (map[interface{}]interface{}, map[interface{}]interface{}, map[interface{}]interface{}, string, string, error) {
	l := Logger.Wrap("vault restore %s", instanceID)
	state := struct {
		Manifest      map[string]interface{} `json:"manifest"`
		Credentials   map[string]interface{} `json:"credentials"`
		Params        map[string]interface{} `json:"params"`
		InitScript    string                 `json:"init"`
		UpgradeScript string                 `json:"upgrade"`
	}{}

	manifest := make(map[interface{}]interface{})
	credentials := make(map[interface{}]interface{})
	params := make(map[interface{}]interface{})
	initFile := ""
	upgradeFile := ""

	exists, err := vault.Get(fmt.Sprintf("%s/state", instanceID), &state)
	if err != nil || !exists {
		l.Error("unable to find service instance %s in vault index", instanceID)
		return manifest, credentials, params, initFile, upgradeFile, err
	}

	return manifest, credentials, params, initFile, upgradeFile, nil
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
	type TaskState struct {
		Action string                 `json:"action"`
		Task   int                    `json:"task"`
		Params map[string]interface{} `json:"params"`
	}

	state := TaskState{}

	exists, err := vault.Get(fmt.Sprintf("%s/task", instanceID), &state)
	if err == nil && !exists {
		err = fmt.Errorf("Instance %s not found in Vault", instanceID)
	}

	return state.Action, state.Task, state.Params, err
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

func (vault *Vault) updateHomeDirs() {
	home := os.Getenv("BLACKSMITH_OPER_HOME")
	if home == "" {
		return
	}

	l := Logger.Wrap("update-home")

	/* ~/.saferc */
	path := fmt.Sprintf("%s/.saferc", home)
	l.Debug("writing ~/.saferc file to %s", path)
	var saferc struct {
		Version int    `json:"version"`
		Current string `json:"current"`
		Vaults  struct {
			Local struct {
				URL   string `json:"url"`
				Token string `json:"token"`
			} `json:"blacksmith"`
		} `json:"vaults"`
	}
	saferc.Version = 1
	saferc.Current = "blacksmith"
	saferc.Vaults.Local.URL = vault.URL
	saferc.Vaults.Local.Token = vault.Token

	b, err := json.Marshal(saferc)
	if err != nil {
		l.Error("failed to marshal new ~/.saferc: %s", err)
	} else {
		err = ioutil.WriteFile(path, b, 0666)
		if err != nil {
			l.Error("failed to write new ~/.saferc: %s", err)
		}
	}

	/* ~/.svtoken */
	path = fmt.Sprintf("%s/.svtoken", home)
	l.Debug("writing ~/.svtoken file to %s", path)
	var svtoken struct {
		Vault string `json:"vault"`
		Token string `json:"token"`
	}
	svtoken.Vault = vault.URL
	svtoken.Token = vault.Token

	b, err = json.Marshal(svtoken)
	if err != nil {
		l.Error("failed to marshal new ~/.svtoken: %s", err)
	} else {
		err = ioutil.WriteFile(path, b, 0666)
		if err != nil {
			l.Error("failed to write new ~/.svtoken: %s", err)
		}
	}

	/* ~/.vault-token */
	path = fmt.Sprintf("%s/.vault-token", home)
	l.Debug("writing ~/.vault-token file to %s", path)
	err = ioutil.WriteFile(path, []byte(vault.Token), 0666)
	if err != nil {
		l.Error("failed to write new ~/.vault-token: %s", err)
	}
}
