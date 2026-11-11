package nodedeploy

import (
	"context"
	"strings"

	"github.com/ish-hcc/ai-agent-proto/internal/model"
	"github.com/ish-hcc/ai-agent-proto/internal/tumblebug"
)

func checkPlacementReview(node *tumblebug.Node, review *model.NodePlacementReview) error {
	if review == nil {
		return nil
	}
	if node == nil || node.SpecID != review.SpecID || node.ImageID != review.ImageID {
		return &model.ValidationError{Message: "the target node does not match the reviewed CB-Tumblebug spec and image"}
	}
	if !strings.EqualFold(node.Status, "Running") {
		return &model.ValidationError{Message: "the reviewed CB-Tumblebug node must be running"}
	}
	return nil
}

func (s *Service) checkApprovedGPUVendor(ctx context.Context, specID string, spec *tumblebug.SpecInfo) error {
	if spec == nil && specID != "" {
		// A request with an explicit GPU flag has not read the spec yet.
		var err error
		spec, err = s.tb.GetSpec(ctx, s.opts.SpecNamespace, specID)
		if err != nil {
			return ctx.Err()
		}
	}
	if spec != nil {
		vendor := model.AcceleratorVendorOf(spec.AcceleratorModel)
		if vendor != "" && vendor != "nvidia" {
			return &model.ValidationError{Message: "the approved GPU runtime requires the NVIDIA container driver. CB-Tumblebug reports " + vendor}
		}
	}
	return nil
}
