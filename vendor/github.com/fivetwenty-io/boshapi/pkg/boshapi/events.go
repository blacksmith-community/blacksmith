package boshapi

import "net/url"

// Event represents a BOSH Director event.
type Event struct {
	ID             string                 `json:"id"`
	ParentID       string                 `json:"parent_id,omitempty"`
	Timestamp      int64                  `json:"timestamp"`
	User           string                 `json:"user"`
	Action         string                 `json:"action"`
	ObjectType     string                 `json:"object_type"`
	ObjectName     string                 `json:"object_name"`
	TaskID         string                 `json:"task"`
	DeploymentName string                 `json:"deployment"`
	Instance       string                 `json:"instance"`
	Context        map[string]interface{} `json:"context,omitempty"`
	Error          string                 `json:"error,omitempty"`
}

// EventListOptions represents options for listing events.
type EventListOptions struct {
	BeforeID   string
	AfterTime  string
	Before     string
	After      string
	Deployment string
	Task       string
	Instance   string
	User       string
	Action     string
	ObjectType string
	ObjectName string
}

// ToValues converts EventListOptions to url.Values.
func (o *EventListOptions) ToValues() url.Values {
	if o == nil {
		return nil
	}

	values := url.Values{}

	if o.BeforeID != "" {
		values.Set("before_id", o.BeforeID)
	}

	if o.Before != "" {
		values.Set("before_time", o.Before)
	}

	if o.After != "" {
		values.Set("after_time", o.After)
	}

	if o.Deployment != "" {
		values.Set("deployment", o.Deployment)
	}

	if o.Task != "" {
		values.Set("task", o.Task)
	}

	if o.Instance != "" {
		values.Set("instance", o.Instance)
	}

	if o.User != "" {
		values.Set("user", o.User)
	}

	if o.Action != "" {
		values.Set("action", o.Action)
	}

	if o.ObjectType != "" {
		values.Set("object_type", o.ObjectType)
	}

	if o.ObjectName != "" {
		values.Set("object_name", o.ObjectName)
	}

	return values
}

// CreateEventRequest represents a request to create a custom event.
type CreateEventRequest struct {
	Timestamp      int64                  `json:"timestamp,omitempty"`
	User           string                 `json:"user"`
	Action         string                 `json:"action"`
	ObjectType     string                 `json:"object_type"`
	ObjectName     string                 `json:"object_name,omitempty"`
	DeploymentName string                 `json:"deployment,omitempty"`
	Instance       string                 `json:"instance,omitempty"`
	TaskID         string                 `json:"task,omitempty"`
	Context        map[string]interface{} `json:"context,omitempty"`
	Error          string                 `json:"error,omitempty"`
}
