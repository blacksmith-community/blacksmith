package cf

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
)

// Static errors for err113 compliance.
var (
	ErrFailedToGetServiceBrokers          = errors.New("failed to get service brokers")
	ErrFailedToCreateServiceBroker        = errors.New("failed to create service broker")
	ErrFailedToUpdateServiceBroker        = errors.New("failed to update service broker")
	ErrFailedToDeleteServiceBroker        = errors.New("failed to delete service broker")
	ErrFailedToGetServiceOfferings        = errors.New("failed to get service offerings")
	ErrFailedToGetServicePlans            = errors.New("failed to get service plans")
	ErrDisableServiceAccessNotImplemented = errors.New("disable service access not yet implemented")
	ErrServiceBrokerNotFound              = errors.New("service broker not found")
)

// Logger interface for logging.
type Logger interface {
	Warnf(format string, args ...interface{})
}

// noOpLogger is a logger that does nothing.
type noOpLogger struct{}

func (n *noOpLogger) Warnf(format string, args ...interface{}) {}

// Client represents a Cloud Foundry API client.
type Client struct {
	authClient *CFAuthClient
	brokerURL  string
	brokerUser string
	brokerPass string
	logger     Logger
}

// NewClient creates a new CF API client.
func NewClient(apiURL, username, password, brokerURL, brokerUser, brokerPass string) *Client {
	return &Client{
		authClient: NewCFAuthClient(apiURL, username, password),
		brokerURL:  brokerURL,
		brokerUser: brokerUser,
		brokerPass: brokerPass,
		logger:     &noOpLogger{},
	}
}

// TestConnection tests the CF connection.
func (c *Client) TestConnection() (*RegistrationTestResult, error) {
	cfInfo, testErr := c.authClient.TestConnection()

	// Build result based on test outcome
	result := &RegistrationTestResult{
		Success: testErr == nil,
		CFInfo:  cfInfo,
	}
	if testErr != nil {
		result.Message = "Connection failed"
		result.Error = testErr.Error()
	} else {
		result.Message = "Connection successful"
	}

	return result, nil
}

// GetServiceBrokers retrieves all service brokers from CF.
func (c *Client) GetServiceBrokers() ([]BrokerInfo, error) {
	resp, err := c.authClient.MakeAuthenticatedRequest("GET", "/v3/service_brokers", nil)
	if err != nil {
		return nil, err
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: status %d", ErrFailedToGetServiceBrokers, resp.StatusCode)
	}

	var response struct {
		Resources []struct {
			GUID      string `json:"guid"`
			Name      string `json:"name"`
			URL       string `json:"url"`
			Username  string `json:"username"`
			State     string `json:"state"`
			CreatedAt string `json:"created_at"`
			UpdatedAt string `json:"updated_at"`
		} `json:"resources"`
	}

	err = json.NewDecoder(resp.Body).Decode(&response)
	if err != nil {
		return nil, fmt.Errorf("failed to decode brokers response: %w", err)
	}

	brokers := make([]BrokerInfo, len(response.Resources))
	for i, broker := range response.Resources {
		brokers[i] = BrokerInfo{
			ID:       broker.GUID,
			Name:     broker.Name,
			URL:      broker.URL,
			Username: broker.Username,
			State:    broker.State,
		}
		// Parse timestamps if needed
	}

	return brokers, nil
}

// FindServiceBroker finds a service broker by name.
func (c *Client) FindServiceBroker(name string) (*BrokerInfo, error) {
	brokers, err := c.GetServiceBrokers()
	if err != nil {
		return nil, err
	}

	for _, broker := range brokers {
		if broker.Name == name {
			return &broker, nil
		}
	}

	return nil, ErrServiceBrokerNotFound
}

// CreateServiceBroker creates a new service broker in CF.
func (c *Client) CreateServiceBroker(name string) (*BrokerInfo, error) {
	brokerData := map[string]interface{}{
		"name":     name,
		"url":      c.brokerURL,
		"username": c.brokerUser,
		"password": c.brokerPass,
	}

	resp, err := c.authClient.MakeAuthenticatedRequest("POST", "/v3/service_brokers", brokerData)
	if err != nil {
		return nil, err
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusCreated {
		var errorResp struct {
			Errors []struct {
				Detail string `json:"detail"`
				Title  string `json:"title"`
				Code   int    `json:"code"`
			} `json:"errors"`
		}

		err := json.NewDecoder(resp.Body).Decode(&errorResp)
		if err != nil {
			c.logger.Warnf("Failed to decode error response: %v", err)
		}

		errorMsg := "Unknown error"
		if len(errorResp.Errors) > 0 {
			errorMsg = errorResp.Errors[0].Detail
		}

		return nil, fmt.Errorf("%w (%d): %s", ErrFailedToCreateServiceBroker, resp.StatusCode, errorMsg)
	}

	var brokerResponse struct {
		GUID     string `json:"guid"`
		Name     string `json:"name"`
		URL      string `json:"url"`
		Username string `json:"username"`
		State    string `json:"state"`
	}

	err = json.NewDecoder(resp.Body).Decode(&brokerResponse)
	if err != nil {
		return nil, fmt.Errorf("failed to decode broker creation response: %w", err)
	}

	return &BrokerInfo{
		ID:       brokerResponse.GUID,
		Name:     brokerResponse.Name,
		URL:      brokerResponse.URL,
		Username: brokerResponse.Username,
		State:    brokerResponse.State,
	}, nil
}

// UpdateServiceBroker updates an existing service broker.
func (c *Client) UpdateServiceBroker(brokerID, name string) (*BrokerInfo, error) {
	brokerData := map[string]interface{}{
		"name":     name,
		"url":      c.brokerURL,
		"username": c.brokerUser,
		"password": c.brokerPass,
	}

	path := "/v3/service_brokers/" + brokerID

	resp, err := c.authClient.MakeAuthenticatedRequest("PATCH", path, brokerData)
	if err != nil {
		return nil, err
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		var errorResp struct {
			Errors []struct {
				Detail string `json:"detail"`
			} `json:"errors"`
		}

		err := json.NewDecoder(resp.Body).Decode(&errorResp)
		if err != nil {
			c.logger.Warnf("Failed to decode error response: %v", err)
		}

		errorMsg := "Unknown error"
		if len(errorResp.Errors) > 0 {
			errorMsg = errorResp.Errors[0].Detail
		}

		return nil, fmt.Errorf("%w (%d): %s", ErrFailedToUpdateServiceBroker, resp.StatusCode, errorMsg)
	}

	var brokerResponse struct {
		GUID     string `json:"guid"`
		Name     string `json:"name"`
		URL      string `json:"url"`
		Username string `json:"username"`
		State    string `json:"state"`
	}

	err = json.NewDecoder(resp.Body).Decode(&brokerResponse)
	if err != nil {
		return nil, fmt.Errorf("failed to decode broker update response: %w", err)
	}

	return &BrokerInfo{
		ID:       brokerResponse.GUID,
		Name:     brokerResponse.Name,
		URL:      brokerResponse.URL,
		Username: brokerResponse.Username,
		State:    brokerResponse.State,
	}, nil
}

// DeleteServiceBroker deletes a service broker from CF.
func (c *Client) DeleteServiceBroker(brokerID string) error {
	path := "/v3/service_brokers/" + brokerID

	resp, err := c.authClient.MakeAuthenticatedRequest("DELETE", path, nil)
	if err != nil {
		return err
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("%w: status %d", ErrFailedToDeleteServiceBroker, resp.StatusCode)
	}

	return nil
}

// GetServiceOfferings retrieves service offerings from CF catalog.
func (c *Client) GetServiceOfferings() ([]ServiceInfo, error) {
	resp, err := c.authClient.MakeAuthenticatedRequest("GET", "/v3/service_offerings", nil)
	if err != nil {
		return nil, err
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: status %d", ErrFailedToGetServiceOfferings, resp.StatusCode)
	}

	var response struct {
		Resources []struct {
			GUID          string `json:"guid"`
			Name          string `json:"name"`
			Description   string `json:"description"`
			Available     bool   `json:"available"`
			Relationships struct {
				ServiceBroker struct {
					Data struct {
						GUID string `json:"guid"`
					} `json:"data"`
				} `json:"service_broker"`
			} `json:"relationships"`
		} `json:"resources"`
	}

	err = json.NewDecoder(resp.Body).Decode(&response)
	if err != nil {
		return nil, fmt.Errorf("failed to decode service offerings response: %w", err)
	}

	services := make([]ServiceInfo, len(response.Resources))
	for i, service := range response.Resources {
		services[i] = ServiceInfo{
			ID:          service.GUID,
			Name:        service.Name,
			Description: service.Description,
			BrokerID:    service.Relationships.ServiceBroker.Data.GUID,
			Active:      service.Available,
		}
	}

	return services, nil
}

// EnableServiceAccess enables access to a service offering.
func (c *Client) EnableServiceAccess(serviceOfferingGUID string) error {
	// Build the query to enable access for all orgs
	params := url.Values{}
	params.Set("service_offering_guids", serviceOfferingGUID)

	// First, get all service plans for this offering
	planPath := "/v3/service_plans?service_offering_guids=" + serviceOfferingGUID

	resp, err := c.authClient.MakeAuthenticatedRequest("GET", planPath, nil)
	if err != nil {
		return err
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: status %d", ErrFailedToGetServicePlans, resp.StatusCode)
	}

	var plansResponse struct {
		Resources []struct {
			GUID string `json:"guid"`
		} `json:"resources"`
	}

	err = json.NewDecoder(resp.Body).Decode(&plansResponse)
	if err != nil {
		return fmt.Errorf("failed to decode service plans response: %w", err)
	}

	// Enable access for each plan
	for _, plan := range plansResponse.Resources {
		visibilityData := map[string]interface{}{
			"type": "public",
			"service_plan": map[string]string{
				"guid": plan.GUID,
			},
		}

		resp, err := c.authClient.MakeAuthenticatedRequest("POST", "/v3/service_plan_visibilities", visibilityData)
		if err != nil {
			return fmt.Errorf("failed to enable access for plan %s: %w", plan.GUID, err)
		}

		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != http.StatusCreated {
			// May already be public, continue
			continue
		}
	}

	return nil
}

// DisableServiceAccess disables access to a service offering.
func (c *Client) DisableServiceAccess(serviceOfferingGUID string) error {
	// Implementation would remove public visibility
	// For now, we'll skip this as it's not in the immediate requirements
	return ErrDisableServiceAccessNotImplemented
}
