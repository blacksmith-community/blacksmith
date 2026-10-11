package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/fivetwenty-io/boshapi/internal/http"
	"github.com/fivetwenty-io/boshapi/pkg/boshapi"
)

type eventsClient struct {
	httpClient *http.Client
}

// NewEventsClient creates a new events client.
func NewEventsClient(httpClient *http.Client) boshapi.EventsClient {
	return &eventsClient{httpClient: httpClient}
}

func (c *eventsClient) List(ctx context.Context, opts *boshapi.EventListOptions) ([]boshapi.Event, error) {
	var query url.Values

	if opts != nil {
		query = opts.ToValues()
	}

	resp, err := c.httpClient.Get(ctx, "/events", query)
	if err != nil {
		return nil, fmt.Errorf("listing events: %w", err)
	}

	var events []boshapi.Event

	if err := json.Unmarshal(resp.Body, &events); err != nil {
		return nil, fmt.Errorf("parsing events: %w", err)
	}

	return events, nil
}

func (c *eventsClient) Get(ctx context.Context, id string) (*boshapi.Event, error) {
	if err := checkPathSegments("Events().Get", "id", id); err != nil {
		return nil, err
	}

	path := "/events/" + url.PathEscape(id)

	resp, err := c.httpClient.Get(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("getting event %s: %w", id, err)
	}

	var event boshapi.Event

	if err := json.Unmarshal(resp.Body, &event); err != nil {
		return nil, fmt.Errorf("parsing event: %w", err)
	}

	return &event, nil
}

func (c *eventsClient) Create(ctx context.Context, req *boshapi.CreateEventRequest) error {
	// The director refuses a missing field with a 400 and keeps an empty one,
	// then drops it when it reads the event back, so an empty value is
	// refused here.
	var missing string

	switch {
	case req == nil:
		missing = "the request"
	case req.Action == "":
		missing = "Action"
	case req.ObjectType == "":
		missing = "ObjectType"
	case req.ObjectName == "":
		missing = "ObjectName"
	}

	if missing != "" {
		return fmt.Errorf("creating event: %w; nothing was sent. %s is empty", boshapi.ErrEventFieldRequired, missing)
	}

	_, err := c.httpClient.Post(ctx, "/events", req)
	if err != nil {
		return fmt.Errorf("creating event: %w", err)
	}

	return nil
}
