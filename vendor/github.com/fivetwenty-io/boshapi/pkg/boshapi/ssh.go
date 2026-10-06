package boshapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// maxSSHOutputExcerpt is the most of an unreadable SSH result that
// ParseSSHResults quotes in its error.
const maxSSHOutputExcerpt = 200

// SSHRequest represents a request to set up or clean up SSH access.
type SSHRequest struct {
	Command        string    `json:"command"`
	DeploymentName string    `json:"deployment_name"`
	Target         SSHTarget `json:"target"`
	Params         SSHParams `json:"params"`
}

// SSHTarget represents the target for SSH. An empty Job is left out of the
// request, because the director treats an empty string as a filter that
// matches no instance group. IDs is always sent, as an empty array when there
// are none, because the director reads it when no index is given and the task
// fails on a missing value.
type SSHTarget struct {
	Job     string   `json:"job,omitempty"`
	Indices []int    `json:"indexes,omitempty"`
	IDs     []string `json:"ids"`
}

// MarshalJSON encodes the target with IDs as an empty array, never null, when
// it holds none.
func (t SSHTarget) MarshalJSON() ([]byte, error) {
	type target SSHTarget

	if t.IDs == nil {
		t.IDs = []string{}
	}

	data, err := json.Marshal(target(t))
	if err != nil {
		return nil, fmt.Errorf("encoding SSH target: %w", err)
	}

	return data, nil
}

// SSHParams represents SSH parameters. Setup sends User and PublicKey, and
// cleanup sends UserRegex, which the director matches against the users it
// created.
type SSHParams struct {
	User      string `json:"user,omitempty"`
	PublicKey string `json:"public_key,omitempty"`
	UserRegex string `json:"user_regex,omitempty"`
}

// SSHResult represents the result of an SSH setup for one instance. Index is
// nil when the director reports a null index.
type SSHResult struct {
	Status        string `json:"status"`
	IP            string `json:"ip"`
	HostPublicKey string `json:"host_public_key"`
	Job           string `json:"job"`
	Index         *int   `json:"index"`
	ID            string `json:"id"`
	GatewayUser   string `json:"gateway_user"`
	GatewayHost   string `json:"gateway_host"`
}

// ParseSSHResults decodes the result output of a finished SSH setup task,
// which is one JSON array with an entry per instance.
func ParseSSHResults(output string) ([]SSHResult, error) {
	trimmed := strings.TrimSpace(output)
	if trimmed == "" {
		return nil, errors.New("SSH task result output is empty: " +
			"check that the SSH setup task finished and that its result output was fetched")
	}

	var results []SSHResult

	if err := json.Unmarshal([]byte(trimmed), &results); err != nil {
		excerpt := strings.ToValidUTF8(trimmed, "")
		if len(excerpt) > maxSSHOutputExcerpt {
			excerpt = strings.ToValidUTF8(excerpt[:maxSSHOutputExcerpt], "") + "..."
		}

		return nil, fmt.Errorf("parsing SSH task result output as a JSON array (output starts %q): %w; "+
			"check that the output is the result of an SSH setup task", excerpt, err)
	}

	return results, nil
}
