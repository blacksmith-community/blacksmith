package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

type Vault struct {
	URL      string
	Token    string
	Insecure bool
	client   *VaultClient // HashiCorp API client
}

// ensureClient ensures the vault client is initialized
func (vault *Vault) ensureClient() error {
	if vault.client == nil {
		client, err := NewVaultClient(vault.URL, vault.Token, vault.Insecure)
		if err != nil {
			return err
		}
		vault.client = client
	}
	return nil
}

type VaultCreds struct {
	SealKey   string `json:"seal_key"`
	RootToken string `json:"root_token"`
}

func (vault *Vault) Init(store string) error {
	l := Logger.Wrap("vault init")

	// Initialize the new client if not already done
	if err := vault.ensureClient(); err != nil {
		l.Error("failed to ensure vault client: %s", err)
		return err
	}

	// Check if vault is already initialized
	initResp, err := vault.client.InitVault(1, 1)
	if err != nil {
		return err
	}

	if initResp == nil {
		// Vault was already initialized, read existing credentials
		l.Debug("reading credentials files from %s", store)
		b, err := os.ReadFile(store)
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
		vault.client.Token = creds.RootToken
		vault.client.SetToken(creds.RootToken)
		os.Setenv("VAULT_TOKEN", vault.Token)
		vault.updateHomeDirs()
		return vault.Unseal(creds.SealKey)
	}

	// Vault was just initialized
	if initResp.RootToken == "" || len(initResp.Keys) != 1 {
		if initResp.RootToken == "" {
			l.Error("failed to initialize vault: root token was blank")
		}
		if len(initResp.Keys) != 1 {
			l.Error("failed to initialize vault: incorrect number of seal keys (%d) returned", len(initResp.Keys))
		}
		err = fmt.Errorf("invalid response from vault: token '%s' and %d keys", initResp.RootToken, len(initResp.Keys))
		return err
	}

	creds := VaultCreds{
		SealKey:   initResp.Keys[0],
		RootToken: initResp.RootToken,
	}
	l.Debug("marshaling credentials for longterm storage")
	b, err := json.Marshal(creds)
	if err != nil {
		l.Error("failed to marshal vault root token / seal key for longterm storage: %s", err)
		return err
	}
	l.Debug("storing credentials at %s (mode 0600)", store)
	err = os.WriteFile(store, b, 0600)
	if err != nil {
		l.Error("failed to write credentials to longterm storage file %s: %s", store, err)
		return err
	}

	vault.Token = creds.RootToken
	vault.client.Token = creds.RootToken
	vault.client.SetToken(creds.RootToken)
	os.Setenv("VAULT_TOKEN", vault.Token)
	vault.updateHomeDirs()
	return vault.Unseal(creds.SealKey)
}

func (vault *Vault) Unseal(key string) error {
	l := Logger.Wrap("vault unseal")

	// Ensure client is initialized
	if err := vault.ensureClient(); err != nil {
		l.Error("failed to ensure vault client: %s", err)
		return err
	}

	// Use the new client to unseal
	return vault.client.UnsealVault(key)
}

func (vault *Vault) VerifyMount(store string, createIfMissing bool) error {
	l := Logger.Wrap("Verify Mount")

	// Ensure client is initialized
	if err := vault.ensureClient(); err != nil {
		l.Error("failed to ensure vault client: %s", err)
		return err
	}

	// Use the new client to verify/create mount
	return vault.client.VerifyMount(store, createIfMissing)
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
	client := &http.Client{}
	return client.Do(req)
}

func (vault *Vault) Get(path string, out interface{}) (bool, error) {
	l := Logger.Wrap("vault get")

	// Ensure client is initialized
	if err := vault.ensureClient(); err != nil {
		l.Error("failed to ensure vault client: %s", err)
		return false, err
	}

	// Get the secret using the new client
	data, exists, err := vault.client.GetSecret(path)
	if err != nil {
		return false, err
	}

	if !exists {
		return false, nil
	}

	if out == nil {
		return true, nil
	}

	// Marshal and unmarshal to populate the output
	dataBytes, err := json.Marshal(data)
	if err != nil {
		l.Error("could not remarshal vault data: %s", err)
		return true, fmt.Errorf("could not remarshal vault data")
	}

	err = json.Unmarshal(dataBytes, &out)
	return true, err
}

func (vault *Vault) Put(path string, data interface{}) error {
	l := Logger.Wrap("vault put")

	// Ensure client is initialized
	if err := vault.ensureClient(); err != nil {
		l.Error("failed to ensure vault client: %s", err)
		return err
	}

	// Convert data to map[string]interface{}
	dataMap, err := convertToMap(data)
	if err != nil {
		l.Error("failed to convert data to map: %s", err)
		return err
	}

	// Put the secret using the new client
	return vault.client.PutSecret(path, dataMap)
}

func (vault *Vault) Delete(path string) error {
	l := Logger.Wrap("vault delete")

	// Ensure client is initialized
	if err := vault.ensureClient(); err != nil {
		l.Error("failed to ensure vault client: %s", err)
		return err
	}

	// Delete the secret using the new client
	return vault.client.DeleteSecret(path)
}

func (vault *Vault) Clear(instanceID string) {
	l := Logger.Wrap("vault clear %s", instanceID)

	// Ensure client is initialized
	if err := vault.ensureClient(); err != nil {
		l.Error("failed to ensure vault client: %s", err)
		return
	}

	var rm func(string)
	rm = func(path string) {
		l.Debug("removing Vault secrets at/below %s", path)

		if err := vault.client.DeleteSecret(path); err != nil {
			l.Error("failed to delete %s: %s", path, err)
		}

		keys, err := vault.client.ListSecrets(path)
		if err != nil {
			l.Error("failed to list secrets at %s: %s", path, err)
			return
		}

		for _, sub := range keys {
			rm(fmt.Sprintf("%s/%s", path, strings.TrimSuffix(sub, "/")))
		}

		l.Debug("cleared out vault secrets")
	}
	l.Info("removing secrets under %s", instanceID)
	rm(instanceID)
	l.Info("completed")
}

func (vault *Vault) Track(instanceID, action string, taskID int, params interface{}) error {
	l := Logger.Wrap("vault track %s", instanceID)
	l.Debug("tracking action '%s', task %d", action, taskID)

	// Determine the state description based on task ID
	var state string
	switch taskID {
	case -1:
		state = "failed"
	case 0:
		state = "initializing"
	default:
		state = "in_progress"
	}

	task := struct {
		Action      string      `json:"action"`
		Task        int         `json:"task"`
		State       string      `json:"state"`
		Description string      `json:"description"`
		UpdatedAt   int64       `json:"updated_at"`
		Params      interface{} `json:"params"`
	}{
		Action:      action,
		Task:        taskID,
		State:       state,
		Description: "",
		UpdatedAt:   time.Now().Unix(),
		Params:      deinterface(params),
	}

	return vault.Put(fmt.Sprintf("%s/task", instanceID), task)
}

// TrackProgress updates the progress of an async operation with a description
func (vault *Vault) TrackProgress(instanceID, action, description string, taskID int, params interface{}) error {
	l := Logger.Wrap("vault track progress %s", instanceID)
	l.Debug("tracking progress for '%s': %s", action, description)

	// Determine the state based on task ID
	var state string
	switch taskID {
	case -1:
		state = "failed"
	case 0:
		state = "initializing"
	default:
		state = "in_progress"
	}

	task := struct {
		Action      string      `json:"action"`
		Task        int         `json:"task"`
		State       string      `json:"state"`
		Description string      `json:"description"`
		UpdatedAt   int64       `json:"updated_at"`
		Params      interface{} `json:"params"`
	}{
		Action:      action,
		Task:        taskID,
		State:       state,
		Description: description,
		UpdatedAt:   time.Now().Unix(),
		Params:      deinterface(params),
	}

	// Store current state
	err := vault.Put(fmt.Sprintf("%s/task", instanceID), task)
	if err != nil {
		return err
	}

	// Append to history
	return vault.AppendHistory(instanceID, action, description)
}

// AppendHistory adds an entry to the operation history for an instance
func (vault *Vault) AppendHistory(instanceID, action, description string) error {
	l := Logger.Wrap("vault history %s", instanceID)
	l.Debug("appending to history: %s - %s", action, description)

	historyPath := fmt.Sprintf("%s/history", instanceID)
	
	// Get existing history
	var historyData map[string]interface{}
	exists, err := vault.Get(historyPath, &historyData)
	if err != nil {
		l.Error("failed to get history: %s", err)
		// Start fresh if there's an error
		historyData = map[string]interface{}{"entries": []map[string]interface{}{}}
	}
	if !exists {
		historyData = map[string]interface{}{"entries": []map[string]interface{}{}}
	}
	
	// Extract the entries array
	var history []map[string]interface{}
	if entries, ok := historyData["entries"].([]interface{}); ok {
		// Convert []interface{} to []map[string]interface{}
		for _, entry := range entries {
			if entryMap, ok := entry.(map[string]interface{}); ok {
				history = append(history, entryMap)
			}
		}
	} else if entries, ok := historyData["entries"].([]map[string]interface{}); ok {
		history = entries
	} else {
		// If entries doesn't exist or is wrong type, start fresh
		history = []map[string]interface{}{}
	}

	// Append new entry
	entry := map[string]interface{}{
		"timestamp":   time.Now().Unix(),
		"action":      action,
		"description": description,
	}
	history = append(history, entry)

	// Limit history to last 50 entries
	if len(history) > 50 {
		history = history[len(history)-50:]
	}

	// Store history wrapped in a map
	historyData = map[string]interface{}{"entries": history}
	return vault.Put(historyPath, historyData)
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
	// Note: We intentionally do NOT call vault.Clear(instanceID) here
	// to preserve secrets for auditing purposes
	return err
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
				URL         string `json:"url"`
				Token       string `json:"token"`
				NoStrongbox bool   `json:"no-strongbox"`
			} `json:"blacksmith"`
		} `json:"vaults"`
	}
	saferc.Version = 1
	saferc.Current = "blacksmith"
	saferc.Vaults.Local.URL = vault.URL
	saferc.Vaults.Local.Token = vault.Token
	saferc.Vaults.Local.NoStrongbox = true

	b, err := json.Marshal(saferc)
	if err != nil {
		l.Error("failed to marshal new ~/.saferc: %s", err)
	} else {
		err = os.WriteFile(path, b, 0666)
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
		err = os.WriteFile(path, b, 0666)
		if err != nil {
			l.Error("failed to write new ~/.svtoken: %s", err)
		}
	}

	/* ~/.vault-token */
	path = fmt.Sprintf("%s/.vault-token", home)
	l.Debug("writing ~/.vault-token file to %s", path)
	err = os.WriteFile(path, []byte(vault.Token), 0666)
	if err != nil {
		l.Error("failed to write new ~/.vault-token: %s", err)
	}
}
