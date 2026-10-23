package tumblebug

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
)

// InfraCmdReq runs shell commands on the nodes of an Infra.
type InfraCmdReq struct {
	Command        []string `json:"command"`
	UserName       string   `json:"userName,omitempty"`
	TimeoutMinutes int      `json:"timeoutMinutes,omitempty"`
}

// CommandResults is the response of a remote command execution.
type CommandResults struct {
	Results []CommandResult `json:"results"`
}

// CommandResult is the outcome of the command set on one node.
//
// Command, Stdout and Stderr are keyed by the command's position in the request,
// as a decimal string, because CB-Tumblebug serializes them as objects rather
// than arrays.
type CommandResult struct {
	InfraID string            `json:"infraId"`
	NodeID  string            `json:"nodeId"`
	NodeIP  string            `json:"nodeIp"`
	Command map[string]string `json:"command"`
	Stdout  map[string]string `json:"stdout"`
	Stderr  map[string]string `json:"stderr"`
	Err     string            `json:"err"`
}

// RunCommand executes shell commands on the nodes of an Infra.
func (c *Client) RunCommand(ctx context.Context, nsID, infraID string, req *InfraCmdReq) (*CommandResults, error) {
	path := fmt.Sprintf("/ns/%s/cmd/infra/%s", url.PathEscape(nsID), url.PathEscape(infraID))

	result := &CommandResults{}
	if err := c.Do(ctx, http.MethodPost, path, nil, req, result); err != nil {
		return nil, fmt.Errorf("failed to run command on infra %q: %w", infraID, err)
	}
	return result, nil
}
