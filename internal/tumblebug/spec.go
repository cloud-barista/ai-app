package tumblebug

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
)

// RecommendSpec asks CB-Tumblebug for node specs matching a filter.
//
// This is the entry point for the AI semiconductor axis: the accelerator fields
// of a spec are filterable metrics, so an application's accelerator requirement
// becomes a filter without any provider specific mapping.
func (c *Client) RecommendSpec(ctx context.Context, req *RecommendSpecReq) ([]SpecInfo, error) {
	var result []SpecInfo
	if err := c.Do(ctx, http.MethodPost, "/recommendSpec", nil, req, &result); err != nil {
		return nil, fmt.Errorf("failed to recommend specs: %w", err)
	}
	if result == nil {
		// No match must serialize as [], not null.
		return []SpecInfo{}, nil
	}
	return result, nil
}

// GetSpec reads one node spec.
//
// Specs live in the namespace that holds the shared spec catalog, not in the
// namespace a deployment is created in, so nsID is passed explicitly.
func (c *Client) GetSpec(ctx context.Context, nsID, specID string) (*SpecInfo, error) {
	path := fmt.Sprintf("/ns/%s/resources/spec/%s", url.PathEscape(nsID), url.PathEscape(specID))

	result := &SpecInfo{}
	if err := c.Do(ctx, http.MethodGet, path, nil, nil, result); err != nil {
		return nil, fmt.Errorf("failed to get spec %q in namespace %q: %w", specID, nsID, err)
	}
	return result, nil
}
