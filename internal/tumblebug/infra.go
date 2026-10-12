package tumblebug

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
)

// Infra query options accepted by CB-Tumblebug.
const (
	optionStatus = "status"
	// deleteOptionTerminate terminates CSP nodes before deleting the records.
	//
	// The "force" option is deliberately not exposed: it drops records without
	// confirming CSP termination, which leaves billed orphan instances behind.
	deleteOptionTerminate = "terminate"
)

func infraPath(nsID, infraID string) string {
	return fmt.Sprintf("/ns/%s/infra/%s", url.PathEscape(nsID), url.PathEscape(infraID))
}

// ReviewInfraDynamic validates a provisioning request without creating anything.
func (c *Client) ReviewInfraDynamic(ctx context.Context, nsID string, req *InfraDynamicReq) (*ReviewResult, error) {
	path := fmt.Sprintf("/ns/%s/infraDynamicReview", url.PathEscape(nsID))

	result := &ReviewResult{}
	if err := c.Do(ctx, http.MethodPost, path, nil, req, result); err != nil {
		return nil, fmt.Errorf("failed to review infra request %q: %w", req.Name, err)
	}
	return result, nil
}

// CreateInfraDynamic provisions an Infra and blocks until provisioning finishes.
//
// CB-Tumblebug performs this synchronously; cancelling the context aborts
// provisioning and can leave partially created, billable resources behind.
func (c *Client) CreateInfraDynamic(ctx context.Context, nsID string, req *InfraDynamicReq) (*InfraInfo, error) {
	path := fmt.Sprintf("/ns/%s/infraDynamic", url.PathEscape(nsID))

	result := &InfraInfo{}
	if err := c.Do(ctx, http.MethodPost, path, nil, req, result); err != nil {
		return nil, fmt.Errorf("failed to create infra %q: %w", req.Name, err)
	}
	return result, nil
}

// GetInfra reads one Infra with its labels and nodes.
//
// A missing Infra answers 500 rather than 404, so the caller cannot tell absence
// from an upstream failure through this call alone.
func (c *Client) GetInfra(ctx context.Context, nsID, infraID string) (*InfraInfo, error) {
	result := &InfraInfo{}
	if err := c.Do(ctx, http.MethodGet, infraPath(nsID, infraID), nil, nil, result); err != nil {
		return nil, fmt.Errorf("failed to get infra %q: %w", infraID, err)
	}
	return result, nil
}

// GetInfraStatus reads the status view of an Infra.
//
// The upstream response wraps the view under a "status" key, so it cannot be
// decoded as an InfraInfo.
func (c *Client) GetInfraStatus(ctx context.Context, nsID, infraID string) (*InfraStatusView, error) {
	query := url.Values{"option": []string{optionStatus}}

	var wrapper struct {
		Status InfraStatusView `json:"status"`
	}
	if err := c.Do(ctx, http.MethodGet, infraPath(nsID, infraID), query, nil, &wrapper); err != nil {
		return nil, fmt.Errorf("failed to get status of infra %q: %w", infraID, err)
	}
	return &wrapper.Status, nil
}

// ListInfra reads every Infra of a namespace with its status.
func (c *Client) ListInfra(ctx context.Context, nsID string) ([]InfraInfo, error) {
	query := url.Values{"option": []string{optionStatus}}
	path := fmt.Sprintf("/ns/%s/infra", url.PathEscape(nsID))

	var result struct {
		Infra []InfraInfo `json:"infra"`
	}
	if err := c.Do(ctx, http.MethodGet, path, query, nil, &result); err != nil {
		return nil, fmt.Errorf("failed to list infra in namespace %q: %w", nsID, err)
	}
	if result.Infra == nil {
		// An empty namespace must serialize as [], not null.
		return []InfraInfo{}, nil
	}
	return result.Infra, nil
}

// ControlInfra applies a lifecycle action to every node of an Infra.
//
// CB-Tumblebug exposes this as GET; this service takes POST inbound because the
// call changes state, and translates it here.
func (c *Client) ControlInfra(ctx context.Context, nsID, infraID, action string) (*IDList, error) {
	query := url.Values{"action": []string{action}}
	path := fmt.Sprintf("/ns/%s/control/infra/%s", url.PathEscape(nsID), url.PathEscape(infraID))

	result := &IDList{}
	if err := c.Do(ctx, http.MethodGet, path, query, nil, result); err != nil {
		return nil, fmt.Errorf("failed to run action %q on infra %q: %w", action, infraID, err)
	}
	return result, nil
}

// DeleteInfra terminates every node of an Infra and removes its records.
func (c *Client) DeleteInfra(ctx context.Context, nsID, infraID string) (*IDList, error) {
	query := url.Values{"option": []string{deleteOptionTerminate}}

	result := &IDList{}
	if err := c.Do(ctx, http.MethodDelete, infraPath(nsID, infraID), query, nil, result); err != nil {
		return nil, fmt.Errorf("failed to delete infra %q: %w", infraID, err)
	}
	return result, nil
}
