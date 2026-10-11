package boshapi

import (
	"encoding/json"
	"fmt"
)

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
// ConsumerName names the external consumer that the link is made for. The
// director also takes the type of that consumer, which is always "external",
// and MarshalJSON adds it.
type CreateLinkRequest struct {
	LinkProviderID string
	ConsumerName   string
	Network        string
}

// MarshalJSON encodes the request in the shape the director reads, with the
// consumer name inside link_consumer.owner_object.
func (r CreateLinkRequest) MarshalJSON() ([]byte, error) {
	type ownerObject struct {
		Name string `json:"name"`
		Type string `json:"type"`
	}

	type linkConsumer struct {
		OwnerObject ownerObject `json:"owner_object"`
	}

	body := struct {
		LinkProviderID string       `json:"link_provider_id"`
		LinkConsumer   linkConsumer `json:"link_consumer"`
		Network        string       `json:"network,omitempty"`
	}{
		LinkProviderID: r.LinkProviderID,
		LinkConsumer:   linkConsumer{OwnerObject: ownerObject{Name: r.ConsumerName, Type: "external"}},
		Network:        r.Network,
	}

	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("encoding the link request: %w", err)
	}

	return encoded, nil
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
