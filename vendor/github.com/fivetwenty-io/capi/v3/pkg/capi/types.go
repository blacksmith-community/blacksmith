package capi

import (
	"time"
)

// Resource represents the base structure for all CF API resources
type Resource struct {
	GUID      string    `json:"guid"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Links     Links     `json:"links"`
}

// Links represents resource links
type Links map[string]Link

// Link represents a single link
type Link struct {
	Href   string `json:"href"`
	Method string `json:"method,omitempty"`
}

// Metadata represents labels and annotations
type Metadata struct {
	Labels      map[string]string `json:"labels,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

// Relationship represents a to-one relationship
type Relationship struct {
	Data *RelationshipData `json:"data,omitempty"`
}

// RelationshipData contains the GUID of the related resource
type RelationshipData struct {
	GUID string `json:"guid"`
}

// ToManyRelationship represents a to-many relationship
type ToManyRelationship struct {
	Data []RelationshipData `json:"data"`
}

// Pagination represents pagination information
type Pagination struct {
	TotalResults int   `json:"total_results"`
	TotalPages   int   `json:"total_pages"`
	First        Link  `json:"first"`
	Last         Link  `json:"last"`
	Next         *Link `json:"next,omitempty"`
	Previous     *Link `json:"previous,omitempty"`
}

// ListResponse represents a paginated list response
type ListResponse[T any] struct {
	Pagination Pagination `json:"pagination"`
	Resources  []T        `json:"resources"`
}

// Include represents include parameters for API requests
type Include []string

// Fields represents field selection parameters
type Fields map[string][]string
