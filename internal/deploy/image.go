package deploy

import (
	"context"
	"fmt"
	"strings"

	"github.com/rs/zerolog/log"

	"github.com/ish-hcc/ai-agent-proto/internal/model"
	"github.com/ish-hcc/ai-agent-proto/internal/tumblebug"
)

// imageCandidates bounds an image search. A handful is enough to prefer an
// accelerator image over a plain one without paging the whole catalog.
const imageCandidates = 20

// acceleratorVendor is the driver vendor an accelerator needs.
//
// The vendor matters because an accelerator image ships one vendor's driver.
// Putting an NVIDIA image on an AMD instance boots, but the driver never binds,
// and the failure only shows up when the application tries to use the device.
type acceleratorVendor string

const (
	vendorNVIDIA     acceleratorVendor = "nvidia"
	vendorAMD        acceleratorVendor = "amd"
	vendorIntel      acceleratorVendor = "intel"
	vendorRebellions acceleratorVendor = "rebellions"
	vendorFuriosa    acceleratorVendor = "furiosa"
	vendorUnknown    acceleratorVendor = ""
)

// vendorKeywords maps a vendor to the words that identify it in a device model
// or an image distribution name. The lists are lowercase and matched as substrings.
//
// The NPU vendors are here for the same reason the GPU vendors are: an image
// carries one vendor's driver, and an image built for one accelerator on a node
// carrying another boots fine and never binds. The domestic NPUs reach the
// application through a vLLM plugin, so the runtime does not change and nothing
// upstream of the image would notice the mismatch.
var vendorKeywords = map[acceleratorVendor][]string{
	vendorNVIDIA:     {"nvidia", "tesla", "geforce"},
	vendorAMD:        {"amd", "radeon", "instinct"},
	vendorIntel:      {"intel", "gaudi", "habana"},
	vendorRebellions: {"rebellions", "rbln", "atom"},
	vendorFuriosa:    {"furiosa", "rngd", "warboy"},
}

// ImageChoice is the image selected for a spec, with why it was selected.
type ImageChoice struct {
	ID       string `json:"id"`
	OSType   string `json:"osType"`
	GPUImage bool   `json:"gpuImage"`
	// Vendor is the accelerator vendor the image was matched against, when known.
	Vendor string `json:"vendor,omitempty"`
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
func (s *Service) ResolveImage(ctx context.Context, app *model.AppSpec, specID string) (*ImageChoice, error) {
	osType := app.Install.OSType
	if osType == "" {
		osType = s.defaultOSType
	}

	req := &tumblebug.SearchImageReq{
		MatchedSpecID: specID,
		OSType:        osType,
		MaxResults:    imageCandidates,
	}
	result, err := s.tumblebug.SearchImage(ctx, s.imageNamespace, req)
	if err != nil {
		return nil, err
	}
	if len(result.ImageList) == 0 {
		return nil, fmt.Errorf("no %s image is registered for spec %q", osType, specID)
	}

	if app.Accelerator.Type == "" {
		chosen := result.ImageList[0]
		return s.chose(app, specID, chosen, "first matching image for the spec"), nil
	}

	vendor := s.acceleratorVendor(ctx, app, specID)
	for _, image := range result.ImageList {
		if !image.IsGPUImage {
			continue
		}
		imageVendor := vendorOf(image.OSDistribution, image.Name, image.ID)
		if imageVendor == vendor {
			choice := s.chose(app, specID, image, "accelerator image: ships the vendor driver")
			choice.Vendor = string(vendor)
			return choice, nil
		}
		if vendor == vendorUnknown {
			// The spec names no vendor. That happens when its accelerator model
			// is a vGPU profile identifier ("VGPU8-2G") rather than a device
			// name, and no rule can recover the vendor from it. Taking the image
			// is still the better bet than a plain one, but the pick was not
			// checked and the reason says so instead of implying it was.
			choice := s.chose(app, specID, image, "accelerator image, but the spec names no accelerator vendor "+
				"so the image vendor was not checked; confirm the driver bound before serving")
			choice.Vendor = string(imageVendor)
			return choice, nil
		}
	}

	// Every accelerator image on offer is for another vendor. A plain image at
	// least does not pretend the driver is there.
	chosen := result.ImageList[0]
	choice := s.chose(app, specID, chosen, fmt.Sprintf(
		"no %s accelerator image is registered for this spec, so the driver must be installed by the application commands",
		vendorLabel(vendor)))
	choice.Vendor = string(vendor)
	return choice, nil
}

// acceleratorVendor reports which vendor's driver the spec's accelerator needs.
//
// The application requirement is the first source because it is what the author
// asked for; the spec is consulted when the requirement names no model, which is
// the common case for a requirement written as "any GPU".
func (s *Service) acceleratorVendor(ctx context.Context, app *model.AppSpec, specID string) acceleratorVendor {
	if vendor := vendorOf(app.Accelerator.Model); vendor != vendorUnknown {
		return vendor
	}

	spec, err := s.tumblebug.GetSpec(ctx, s.specNamespace, specID)
	if err != nil {
		// Not knowing the vendor is not a reason to fail the deployment: it only
		// costs the vendor check, and the caller is told which case applied.
		log.Warn().Err(err).Str("specId", specID).
			Msg("Could not read spec, skipping the accelerator vendor check")
		return vendorUnknown
	}
	return vendorOf(spec.AcceleratorModel)
}

// chose builds an ImageChoice and logs the pick.
func (s *Service) chose(app *model.AppSpec, specID string, image tumblebug.ImageInfo, reason string) *ImageChoice {
	log.Info().Str("appId", app.ID).Str("specId", specID).Str("imageId", image.ID).
		Bool("gpuImage", image.IsGPUImage).Msg("Resolved node image")

	return &ImageChoice{
		ID:       image.ID,
		OSType:   image.OSType,
		GPUImage: image.IsGPUImage,
		Reason:   reason,
	}
}

// vendorOf reports the first vendor named by any of the given texts.
func vendorOf(texts ...string) acceleratorVendor {
	for _, text := range texts {
		lowered := strings.ToLower(text)
		for vendor, keywords := range vendorKeywords {
			for _, keyword := range keywords {
				if strings.Contains(lowered, keyword) {
					return vendor
				}
			}
		}
	}
	return vendorUnknown
}

// vendorLabel renders a vendor for a caller-facing message.
func vendorLabel(vendor acceleratorVendor) string {
	if vendor == vendorUnknown {
		return "matching"
	}
	return string(vendor)
}
