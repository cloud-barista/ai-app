package tumblebug

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
)

// SearchImageReq asks CB-Tumblebug for images usable with a given spec.
//
// MatchedSpecID is the important field: it constrains the search to images the
// chosen node spec can actually boot, which avoids hand maintaining a provider
// to image table.
type SearchImageReq struct {
	MatchedSpecID         string `json:"matchedSpecId,omitempty"`
	ProviderName          string `json:"providerName,omitempty"`
	RegionName            string `json:"regionName,omitempty"`
	OSType                string `json:"osType,omitempty"`
	OSArchitecture        string `json:"osArchitecture,omitempty"`
	IsGPUImage            bool   `json:"isGPUImage,omitempty"`
	IncludeBasicImageOnly bool   `json:"includeBasicImageOnly,omitempty"`
	MaxResults            int    `json:"maxResults,omitempty"`
}

// SearchImageResp is the image search result.
type SearchImageResp struct {
	ImageCount int         `json:"imageCount"`
	ImageList  []ImageInfo `json:"imageList"`
}

// ImageInfo is one node image as CB-Tumblebug knows it.
type ImageInfo struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	CSPImageName   string `json:"cspImageName"`
	ProviderName   string `json:"providerName"`
	OSType         string `json:"osType"`
	OSArchitecture string `json:"osArchitecture"`
	// OSDistribution names the image build, and is where the accelerator vendor
	// shows up ("Deep Learning ARM64 Base OSS Nvidia Driver GPU AMI ...").
	OSDistribution string `json:"osDistribution"`
	// IsGPUImage marks an image that already carries accelerator drivers, which
	// is what an AI application needs to start without a driver install step.
	IsGPUImage   bool `json:"isGPUImage"`
	IsBasicImage bool `json:"isBasicImage"`
}

// SearchImage finds node images matching the given conditions.
//
// Images live in the namespace that holds the shared image catalog, which is not
// the namespace a deployment is created in, so nsID is passed explicitly.
func (c *Client) SearchImage(ctx context.Context, nsID string, req *SearchImageReq) (*SearchImageResp, error) {
	path := fmt.Sprintf("/ns/%s/resources/searchImage", url.PathEscape(nsID))

	result := &SearchImageResp{}
	if err := c.Do(ctx, http.MethodPost, path, nil, req, result); err != nil {
		return nil, fmt.Errorf("failed to search images in namespace %q: %w", nsID, err)
	}
	if result.ImageList == nil {
		// No match must serialize as [], not null.
		result.ImageList = []ImageInfo{}
	}
	return result, nil
}
