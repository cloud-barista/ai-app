package infrarec

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/rs/zerolog/log"

	"github.com/ish-hcc/ai-agent-proto/internal/model"
	"github.com/ish-hcc/ai-agent-proto/internal/tumblebug"
)

// imageCandidates bounds an image search. A handful is enough to prefer an
// accelerator image over a plain one without paging the whole catalog.
const imageCandidates = 20

var (
	errSpecNotFound      = errors.New("spec was not found in CB-Tumblebug")
	errSpecMismatch      = errors.New("spec does not meet the application requirements")
	errNoCompatibleImage = errors.New("no compatible image is registered for this spec and OS")
)

// ImageChoice is the image selected for a spec, with why it was selected.
type ImageChoice struct {
	ID       string `json:"id"`
	OSType   string `json:"osType"`
	GPUImage bool   `json:"gpuImage"`
	// Vendor is the accelerator vendor the image was matched against, when known.
	Vendor       string `json:"vendor,omitempty"`
	DriverStatus string `json:"driverStatus"`
	// Reason explains the pick so an agent can report it and an operator can argue with it.
	Reason string `json:"reason"`
}

// ResolveImage picks a node image for an application on a given spec.
//
// An accelerator image is preferred when the application needs an accelerator,
// because it ships the vendor driver and the install commands then do not have
// to. It is only preferred when its vendor matches the accelerator the spec
// actually carries; a mismatch is worse than a plain image, since the node comes
// up looking ready while the driver never binds. Falling back is not silent:
// the reason says which case applied.
func (r *Recommender) ResolveImage(ctx context.Context, app *App, specID string) (*ImageChoice, error) {
	spec, err := r.tumblebug.GetSpec(ctx, r.specNamespace, specID)
	if missingCatalogSpec(err) {
		return nil, errSpecNotFound
	}
	if err != nil {
		return nil, err
	}
	if mismatch := requirementMismatch(app, spec); mismatch != "" {
		return nil, fmt.Errorf("%w (%s)", errSpecMismatch, mismatch)
	}

	osType := app.Install.OSType
	if osType == "" {
		osType = r.defaultOSType
	}

	req := &tumblebug.SearchImageReq{
		MatchedSpecID: specID,
		OSType:        osType,
		MaxResults:    imageCandidates,
	}
	result, err := r.tumblebug.SearchImage(ctx, r.imageNamespace, req)
	if err != nil {
		return nil, err
	}
	vendor := model.AcceleratorVendorOf(spec.AcceleratorModel)
	var plain, unchecked *tumblebug.ImageInfo
	for _, image := range result.ImageList {
		if image.ID == "" {
			continue
		}
		if app.Accelerator.Type == "" {
			return r.chose(app, specID, image, "not_required", "first matching image for the spec"), nil
		}
		if !image.IsGPUImage {
			if plain == nil {
				plain = &image
			}
			continue
		}
		if vendor != "" && model.AcceleratorVendorOf(image.OSDistribution, image.Name, image.CSPImageName, image.ID) == vendor {
			choice := r.chose(app, specID, image, "matched", "accelerator image: catalog names the spec's driver vendor")
			choice.Vendor = vendor
			return choice, nil
		}
		if vendor == "" && unchecked == nil {
			unchecked = &image
		}
	}
	if plain != nil {
		choice := r.chose(app, specID, *plain, "review_required", fmt.Sprintf(
			"no %s accelerator image is registered for this spec; confirm driver installation for the plain image",
			vendorLabel(vendor)))
		choice.Vendor = vendor
		return choice, nil
	}
	if unchecked != nil {
		return r.chose(app, specID, *unchecked, "review_required",
			"spec names no accelerator vendor; confirm the driver against the actual device"), nil
	}
	return nil, errNoCompatibleImage
}

func missingCatalogSpec(err error) bool {
	var upstream *tumblebug.APIError
	if !errors.As(err, &upstream) {
		return false
	}
	if upstream.StatusCode == http.StatusNotFound {
		return true
	}
	// The CB-Tumblebug spec handler also reports missing rows with this 500 response.
	return upstream.StatusCode == http.StatusInternalServerError &&
		strings.HasPrefix(upstream.Message, "The specKey ") &&
		strings.HasSuffix(upstream.Message, " not found by any of ID, CspSpecName")
}

// chose builds an ImageChoice and logs the pick.
func (r *Recommender) chose(app *App, specID string, image tumblebug.ImageInfo, driverStatus, reason string) *ImageChoice {
	log.Info().Str("appId", app.ID).Str("specId", specID).Str("imageId", image.ID).
		Bool("gpuImage", image.IsGPUImage).Msg("Resolved node image")

	return &ImageChoice{
		ID:           image.ID,
		OSType:       image.OSType,
		GPUImage:     image.IsGPUImage,
		DriverStatus: driverStatus,
		Reason:       reason,
	}
}

// vendorLabel renders a vendor for a caller-facing message.
func vendorLabel(vendor string) string {
	if vendor == "" {
		return "matching"
	}
	return vendor
}
