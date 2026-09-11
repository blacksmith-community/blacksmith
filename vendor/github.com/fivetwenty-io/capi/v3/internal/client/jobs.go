package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/fivetwenty-io/capi/v3/internal/constants"
	"github.com/fivetwenty-io/capi/v3/internal/http"
	"github.com/fivetwenty-io/capi/v3/pkg/capi"
)

// JobsClient implements capi.JobsClient.
// Static errors for err113 compliance.
var (
	ErrJobFailed = errors.New("job failed")
)

type JobsClient struct {
	httpClient   *http.Client
	pollInterval time.Duration
	pollTimeout  time.Duration
}

// NewJobsClient creates a new jobs client.
func NewJobsClient(httpClient *http.Client) *JobsClient {
	return &JobsClient{
		httpClient:   httpClient,
		pollInterval: constants.DefaultPollInterval,   // Default poll interval
		pollTimeout:  constants.DefaultJobPollTimeout, // Default poll timeout
	}
}

// Get implements capi.JobsClient.Get.
func (c *JobsClient) Get(ctx context.Context, guid string) (*capi.Job, error) {
	path := "/v3/jobs/" + guid

	resp, err := c.httpClient.Get(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("getting job: %w", err)
	}

	var job capi.Job

	err = json.Unmarshal(resp.Body, &job)
	if err != nil {
		return nil, fmt.Errorf("parsing job: %w", err)
	}

	return &job, nil
}

// PollUntilComplete implements capi.JobsClient.PollUntilComplete
// It polls the job until it reaches a terminal state (COMPLETE or FAILED).
func (c *JobsClient) PollUntilComplete(ctx context.Context, guid string) (*capi.Job, error) {
	// Create a timeout context if not already provided
	pollCtx, cancel := context.WithTimeout(ctx, c.pollTimeout)
	defer cancel()

	ticker := time.NewTicker(c.pollInterval)
	defer ticker.Stop()

	// First check immediately
	job, err := c.Get(pollCtx, guid)
	if err != nil {
		return nil, fmt.Errorf("getting job status: %w", err)
	}

	// Check if already in terminal state
	if isJobComplete(job) {
		if job.State == constants.JobStateFailed {
			return job, fmt.Errorf("%w: %s", ErrJobFailed, formatJobErrors(job))
		}

		return job, nil
	}

	// Poll until complete or timeout
	for {
		select {
		case <-pollCtx.Done():
			// Return the last known state on timeout
			return job, fmt.Errorf("timeout waiting for job to complete: %w", pollCtx.Err())
		case <-ticker.C:
			job, err = c.Get(pollCtx, guid)
			if err != nil {
				return nil, fmt.Errorf("getting job status: %w", err)
			}

			if isJobComplete(job) {
				if job.State == constants.JobStateFailed {
					return job, fmt.Errorf("%w: %s", ErrJobFailed, formatJobErrors(job))
				}

				return job, nil
			}
		}
	}
}

// isJobComplete checks if a job is in a terminal state.
func isJobComplete(job *capi.Job) bool {
	return job.State == "COMPLETE" || job.State == constants.JobStateFailed
}

// formatJobErrors formats job errors for display.
func formatJobErrors(job *capi.Job) string {
	if len(job.Errors) == 0 {
		return "no error details available"
	}

	if len(job.Errors) == 1 {
		return job.Errors[0].Detail
	}

	// Multiple errors
	var errBuilder strings.Builder

	errBuilder.WriteString("multiple errors:")

	for i, err := range job.Errors {
		fmt.Fprintf(&errBuilder, "\n  %d. %s", i+1, err.Detail)
	}

	return errBuilder.String()
}
