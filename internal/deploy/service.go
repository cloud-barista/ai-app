package deploy

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/ish-hcc/ai-agent-proto/internal/catalog"
	"github.com/ish-hcc/ai-agent-proto/internal/config"
	"github.com/ish-hcc/ai-agent-proto/internal/model"
	"github.com/ish-hcc/ai-agent-proto/internal/tumblebug"
)

// Provisioning defaults applied when a request leaves them unset.
const (
	defaultNodeCount     = 1
	partialFailurePolicy = "refine"
	installMonAgent      = "no"
)

// ErrAlreadyExists reports that the target Infra name is taken.
//
// The Infra name is the idempotency key: an agent that retries, or an operator
// who resubmits, must not end up paying for a second copy.
var ErrAlreadyExists = errors.New("infra already exists")

// ErrNotViable reports that CB-Tumblebug rejected the request during review.
var ErrNotViable = errors.New("provisioning request is not viable")

// Service places AI applications on AI semiconductor nodes through CB-Tumblebug.
type Service struct {
	tumblebug *tumblebug.Client
	catalog   *catalog.Store
	// imageNamespace holds the shared image catalog images are searched in.
	imageNamespace string
	// specNamespace holds the shared spec catalog specs are read from.
	specNamespace string
	// defaultOSType is the image family used when an application does not name one.
	defaultOSType string
	// dryRun blocks every call that would change infrastructure. It is a service
	// level setting rather than a per-request flag so that a planning model
	// cannot talk its way past the guard.
	dryRun bool
}

// NewService builds a Service.
func NewService(client *tumblebug.Client, store *catalog.Store, cfg config.TumblebugConfig) *Service {
	return &Service{
		tumblebug:      client,
		catalog:        store,
		imageNamespace: cfg.ImageNamespace,
		specNamespace:  cfg.SpecNamespace,
		defaultOSType:  cfg.DefaultOSType,
		dryRun:         cfg.DryRun,
	}
}

// DryRun reports whether infrastructure changing calls are blocked.
func (s *Service) DryRun() bool { return s.dryRun }

// Plan turns a deployment request into the CB-Tumblebug request it would send.
//
// Planning never changes anything, so it runs regardless of the dry-run guard.
// When the caller did not pin a spec, the accelerator requirement drives a
// recommendation and the cheapest match is taken.
func (s *Service) Plan(ctx context.Context, nsID string, req *model.DeployAppReq) (*model.DeploymentPlan, error) {
	app, err := s.catalog.Get(ctx, req.AppID)
	if err != nil {
		return nil, err
	}

	specID := req.SpecID
	specSource := "requested"
	acceleratorSummary := ""
	if specID == "" {
		candidates, err := s.RecommendSpecs(ctx, app, defaultSpecCandidates)
		if err != nil {
			return nil, err
		}
		if len(candidates) == 0 {
			return nil, fmt.Errorf("no node spec satisfies the accelerator requirement of %q", app.ID)
		}
		// RecommendSpecs orders by cost, so the first candidate is the cheapest match.
		specID = candidates[0].ID
		specSource = "recommended"
		acceleratorSummary = summarizeAccelerator(candidates[0])
	}

	nodeCount := req.NodeCount
	if nodeCount <= 0 {
		nodeCount = defaultNodeCount
	}
	imageID := req.ImageID
	imageSource := "requested"
	imageReason := ""
	if imageID == "" {
		image, err := s.ResolveImage(ctx, app, specID)
		if err != nil {
			return nil, err
		}
		imageID = image.ID
		imageSource = "resolved"
		imageReason = image.Reason
	}
	// RootDiskGiB is a floor the application asks for, not a size to invent. When
	// it is unset the field is left out so the node image keeps its own disk size:
	// a GPU image whose snapshot is larger than a guessed default is rejected by
	// the CSP with InvalidBlockDeviceMapping, and the guess buys nothing.
	rootDisk := app.Resources.RootDiskGiB

	infraReq := &tumblebug.InfraDynamicReq{
		Name:                   req.InfraName,
		Description:            fmt.Sprintf("AI application %s %s", app.ID, app.Version),
		PolicyOnPartialFailure: partialFailurePolicy,
		InstallMonAgent:        installMonAgent,
		SGTemplateID:           req.SGTemplateID,
		NodeGroups: []tumblebug.CreateNodeGroupDynamicReq{{
			Name:          "serving",
			NodeGroupSize: nodeCount,
			SpecID:        specID,
			ImageID:       imageID,
			RootDiskSize:  rootDisk,
			Description:   app.Name,
		}},
		// The install commands ride along with provisioning, so there is no second
		// channel to secure and no window where a node is up but unconfigured.
		PostCommands: []tumblebug.PostCommandReq{{
			Command:        app.Install.Commands,
			UserName:       app.Install.UserName,
			TimeoutMinutes: app.Install.TimeoutMinutes,
		}},
		Label: deploymentLabels(app),
	}

	return &model.DeploymentPlan{
		AppID:              app.ID,
		AppName:            app.Name,
		Namespace:          nsID,
		InfraName:          req.InfraName,
		SpecID:             specID,
		ImageID:            imageID,
		SGTemplateID:       req.SGTemplateID,
		NodeCount:          nodeCount,
		SpecSource:         specSource,
		ImageSource:        imageSource,
		ImageReason:        imageReason,
		AcceleratorSummary: acceleratorSummary,
		Request:            infraReq,
	}, nil
}

// Review asks CB-Tumblebug to validate a plan without creating anything.
func (s *Service) Review(ctx context.Context, plan *model.DeploymentPlan) (*model.ReviewSummary, error) {
	infraReq, ok := plan.Request.(*tumblebug.InfraDynamicReq)
	if !ok {
		return nil, fmt.Errorf("deployment plan for %q carries no provisioning request", plan.InfraName)
	}

	result, err := s.tumblebug.ReviewInfraDynamic(ctx, plan.Namespace, infraReq)
	if err != nil {
		return nil, err
	}

	log.Info().Str("nsId", plan.Namespace).Str("infraName", plan.InfraName).
		Bool("creationViable", result.CreationViable).Str("overallStatus", result.OverallStatus).
		Str("estimatedCost", result.EstimatedCost).Msg("Reviewed deployment plan")

	return &model.ReviewSummary{
		CreationViable:  result.CreationViable,
		OverallStatus:   result.OverallStatus,
		OverallMessage:  result.OverallMessage,
		EstimatedCost:   result.EstimatedCost,
		TotalNodeCount:  result.TotalNodeCount,
		Recommendations: result.Recommendations,
	}, nil
}

// Deploy plans, reviews and, when the guard allows it, provisions.
//
// The order is fixed on purpose: a review always runs before a create, so a
// rejected request never reaches the CSP, and a dry run and a real run produce
// the same plan and the same review for comparison.
func (s *Service) Deploy(ctx context.Context, nsID string, req *model.DeployAppReq) (*model.DeploymentResult, error) {
	plan, err := s.Plan(ctx, nsID, req)
	if err != nil {
		return nil, err
	}

	if err := s.ensureAbsent(ctx, nsID, req.InfraName); err != nil {
		return nil, err
	}

	review, err := s.Review(ctx, plan)
	if err != nil {
		return nil, err
	}
	plan.EstimatedCost = review.EstimatedCost

	if !review.CreationViable {
		return nil, fmt.Errorf("%w: %s (%s)", ErrNotViable, review.OverallMessage, review.OverallStatus)
	}

	if s.dryRun {
		log.Warn().Str("nsId", nsID).Str("infraName", req.InfraName).
			Msg("Dry run: provisioning request prepared but not sent")
		return &model.DeploymentResult{
			Plan:    plan,
			DryRun:  true,
			Review:  review,
			Message: fmt.Sprintf("Dry run: %s reviewed but not provisioned", req.InfraName),
		}, nil
	}

	infraReq, ok := plan.Request.(*tumblebug.InfraDynamicReq)
	if !ok {
		return nil, fmt.Errorf("deployment plan for %q carries no provisioning request", plan.InfraName)
	}

	started := time.Now()
	created, err := s.tumblebug.CreateInfraDynamic(ctx, nsID, infraReq)
	if err != nil {
		return nil, err
	}
	elapsed := time.Since(started).Round(time.Second)

	log.Info().Str("nsId", nsID).Str("infraId", created.ID).Str("status", created.Status).
		Str("appId", req.AppID).Dur("elapsed", elapsed).Msg("Deployed AI application")

	app, err := s.catalog.Get(ctx, req.AppID)
	if err != nil {
		return nil, err
	}
	serving, err := s.OpenServingPort(ctx, nsID, app, created)
	if err != nil {
		return nil, err
	}

	// The nodes are up either way, so a probe that cannot answer is reported as
	// not probed rather than turned into a failed deployment.
	accelerator, err := s.ProbeAccelerator(ctx, nsID, created.ID, app)
	if err != nil {
		log.Warn().Err(err).Str("nsId", nsID).Str("infraId", created.ID).
			Msg("Could not read the accelerator on the deployed nodes")
		accelerator = &model.AcceleratorReport{
			InfraID:  created.ID,
			Mode:     "unknown",
			Devices:  []model.AcceleratorDevice{},
			Findings: []string{},
			Message:  "The accelerator could not be read on the deployed nodes",
		}
	}

	return &model.DeploymentResult{
		Plan:        plan,
		DryRun:      false,
		Review:      review,
		Infra:       created,
		Serving:     serving,
		Accelerator: accelerator,
		Message:     fmt.Sprintf("%s deployed (%s)", req.InfraName, elapsed),
	}, nil
}

// Control applies a lifecycle action to a deployed application.
func (s *Service) Control(ctx context.Context, nsID, infraID, action string) (*model.ControlResult, error) {
	if s.dryRun {
		log.Warn().Str("nsId", nsID).Str("infraId", infraID).Str("action", action).
			Msg("Dry run: lifecycle action prepared but not sent")
		return &model.ControlResult{
			InfraID: infraID,
			Action:  action,
			DryRun:  true,
			Message: fmt.Sprintf("Dry run: %s not applied to %s", action, infraID),
		}, nil
	}

	result, err := s.tumblebug.ControlInfra(ctx, nsID, infraID, action)
	if err != nil {
		return nil, err
	}

	log.Info().Str("nsId", nsID).Str("infraId", infraID).Str("action", action).
		Int("affected", len(result.Output)).Msg("Controlled AI application")

	return &model.ControlResult{
		InfraID:  infraID,
		Action:   action,
		DryRun:   false,
		Affected: result.Output,
		Message:  fmt.Sprintf("%s applied to %s", action, infraID),
	}, nil
}

// Delete terminates the nodes of a deployment and removes its records.
func (s *Service) Delete(ctx context.Context, nsID, infraID string) (*model.ControlResult, error) {
	if s.dryRun {
		log.Warn().Str("nsId", nsID).Str("infraId", infraID).
			Msg("Dry run: deletion prepared but not sent")
		return &model.ControlResult{
			InfraID: infraID,
			Action:  "delete",
			DryRun:  true,
			Message: fmt.Sprintf("Dry run: %s not deleted", infraID),
		}, nil
	}

	result, err := s.tumblebug.DeleteInfra(ctx, nsID, infraID)
	if err != nil {
		return nil, err
	}

	log.Info().Str("nsId", nsID).Str("infraId", infraID).Int("deleted", len(result.Output)).
		Msg("Deleted AI application deployment")

	return &model.ControlResult{
		InfraID:  infraID,
		Action:   "delete",
		DryRun:   false,
		Affected: result.Output,
		Message:  fmt.Sprintf("%s deleted", infraID),
	}, nil
}

// List reads every deployment of a namespace.
func (s *Service) List(ctx context.Context, nsID string) ([]tumblebug.InfraInfo, error) {
	return s.tumblebug.ListInfra(ctx, nsID)
}

// Status reads the node status summary of one deployment.
func (s *Service) Status(ctx context.Context, nsID, infraID string) (*tumblebug.InfraStatusView, error) {
	return s.tumblebug.GetInfraStatus(ctx, nsID, infraID)
}

// ensureAbsent makes the deployment idempotent on the Infra name.
//
// Without this an agent retry, or a resubmitted request, provisions a second
// billable copy that nobody asked for.
//
// The check lists the namespace rather than reading the Infra directly: reading a
// missing Infra answers 500 with "The infra <id> does not exist.", not 404, so a
// direct read cannot tell absence from a real upstream failure. Verified against
// CB-Tumblebug on 2026-09-01.
func (s *Service) ensureAbsent(ctx context.Context, nsID, infraName string) error {
	existing, err := s.tumblebug.ListInfra(ctx, nsID)
	if err != nil {
		return err
	}
	for _, infra := range existing {
		if infra.ID == infraName || infra.Name == infraName {
			return fmt.Errorf("%w: %s", ErrAlreadyExists, infraName)
		}
	}
	return nil
}

func deploymentLabels(app *model.AppSpec) map[string]string {
	labels := map[string]string{
		"ai-mcmp.app-id":      app.ID,
		"ai-mcmp.app-version": app.Version,
		"ai-mcmp.runtime":     string(app.Runtime),
		"ai-mcmp.accelerator": app.Accelerator.Type,
	}
	for key, value := range app.Labels {
		labels[key] = value
	}
	return labels
}

func summarizeAccelerator(candidate model.SpecCandidate) string {
	if candidate.AcceleratorModel == "" {
		return fmt.Sprintf("%s x%d", candidate.AcceleratorType, candidate.AcceleratorCount)
	}
	return fmt.Sprintf("%s %s x%d (%.0f GB)",
		candidate.AcceleratorType, candidate.AcceleratorModel,
		candidate.AcceleratorCount, candidate.AcceleratorMemoryGB)
}
