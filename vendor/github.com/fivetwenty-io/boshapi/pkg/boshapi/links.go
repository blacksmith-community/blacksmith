package boshapi

// Link represents a BOSH link.
type Link struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	LinkConsumerID string `json:"link_consumer_id"`
	LinkProviderID string `json:"link_provider_id"`
	CreatedAt      string `json:"created_at"`
}

// LinkProvider represents a BOSH link provider.
type LinkProvider struct {
	ID                     string                 `json:"id"`
	Name                   string                 `json:"name"`
	Shared                 bool                   `json:"shared"`
	DeploymentName         string                 `json:"deployment"`
	LinkProviderDefinition LinkProviderDefinition `json:"link_provider_definition"`
	OwnerObjectInfo        OwnerObjectInfo        `json:"owner_object"`
}

// LinkProviderDefinition represents a link provider definition.
type LinkProviderDefinition struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// OwnerObjectInfo represents the owner of a link provider/consumer.
type OwnerObjectInfo struct {
	Name string                 `json:"name"`
	Type string                 `json:"type"`
	Info map[string]interface{} `json:"info,omitempty"`
}

// LinkConsumer represents a BOSH link consumer.
type LinkConsumer struct {
	ID              string          `json:"id"`
	Name            string          `json:"name"`
	DeploymentName  string          `json:"deployment"`
	OwnerObjectInfo OwnerObjectInfo `json:"owner_object"`
}

// CreateLinkRequest represents a request to create an external link.
type CreateLinkRequest struct {
	LinkConsumerID string `json:"link_consumer_id"`
	LinkProviderID string `json:"link_provider_id"`
	Network        string `json:"network,omitempty"`
}

// LinkAddressOptions represents options for getting a link address.
type LinkAddressOptions struct {
	LinkID string
	AZs    []string
	Status string
}

// LinkAddress represents a link address response.
type LinkAddress struct {
	Address string `json:"address"`
}
