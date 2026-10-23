package deploy

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/rs/zerolog/log"

	"github.com/ish-hcc/ai-agent-proto/internal/model"
	"github.com/ish-hcc/ai-agent-proto/internal/tumblebug"
)

// maxStderrTail bounds the stderr kept per node in an update result.
const maxStderrTail = 2048

// Provisioner carries out a placement: it reviews, creates and updates Infras.
//
// What runs, in which order, and whether it is allowed to run at all stays in
// Service. A provisioner only decides how a reviewed plan becomes nodes.
type Provisioner interface {
	Review(ctx context.Context, plan *model.DeploymentPlan) (*model.ReviewSummary, error)
	Create(ctx context.Context, nsID string, plan *model.DeploymentPlan) (*model.ProvisionResult, error)
	Update(ctx context.Context, nsID, infraID string, app *model.AppSpec) (*model.ProvisionResult, error)
}

// localProvisioner provisions through CB-Tumblebug directly.
type localProvisioner struct {
	tumblebug *tumblebug.Client
}

// Review asks CB-Tumblebug to validate a plan without creating anything.
func (p *localProvisioner) Review(ctx context.Context, plan *model.DeploymentPlan) (*model.ReviewSummary, error) {
	infraReq, err := infraRequestOf(plan)
	if err != nil {
		return nil, err
	}

	result, err := p.tumblebug.ReviewInfraDynamic(ctx, plan.Namespace, infraReq)
	if err != nil {
		return nil, err
	}

	return &model.ReviewSummary{
		CreationViable:  result.CreationViable,
		OverallStatus:   result.OverallStatus,
		OverallMessage:  result.OverallMessage,
		EstimatedCost:   result.EstimatedCost,
		TotalNodeCount:  result.TotalNodeCount,
		Recommendations: result.Recommendations,
	}, nil
}

// Create provisions the plan through CB-Tumblebug and blocks until it settles.
func (p *localProvisioner) Create(ctx context.Context, nsID string, plan *model.DeploymentPlan) (*model.ProvisionResult, error) {
	infraReq, err := infraRequestOf(plan)
	if err != nil {
		return nil, err
	}

	created, err := p.tumblebug.CreateInfraDynamic(ctx, nsID, infraReq)
	if err != nil {
		return nil, err
	}
	return &model.ProvisionResult{
		InfraID: created.ID,
		Infra:   created,
		Message: fmt.Sprintf("%s created", created.ID),
	}, nil
}

// Update runs the install commands of the given application version on every
// node of the Infra.
//
// The nodes are not reprovisioned: the commands are the same ones provisioning
// ran, sent through the CB-Tumblebug remote command channel the accelerator
// probe already uses.
func (p *localProvisioner) Update(ctx context.Context, nsID, infraID string, app *model.AppSpec) (*model.ProvisionResult, error) {
	req := &tumblebug.InfraCmdReq{
		Command:        app.Install.Commands,
		UserName:       app.Install.UserName,
		TimeoutMinutes: app.Install.TimeoutMinutes,
	}
	results, err := p.tumblebug.RunCommand(ctx, nsID, infraID, req)
	if err != nil {
		return nil, err
	}
	if len(results.Results) == 0 {
		return nil, fmt.Errorf("no node of infra %q answered the update", infraID)
	}

	nodes := make([]model.NodeUpdate, 0, len(results.Results))
	succeeded := 0
	for _, result := range results.Results {
		node := model.NodeUpdate{
			NodeID:    result.NodeID,
			NodeIP:    result.NodeIP,
			Succeeded: result.Err == "",
			Error:     result.Err,
			Stderr:    tail(joinOrdered(result.Stderr), maxStderrTail),
		}
		if node.Succeeded {
			succeeded++
		}
		nodes = append(nodes, node)
	}

	return &model.ProvisionResult{
		InfraID:    infraID,
		AppID:      app.ID,
		AppVersion: app.Version,
		Nodes:      nodes,
		Message: fmt.Sprintf("%s %s applied on %d of %d nodes of %s",
			app.ID, app.Version, succeeded, len(nodes), infraID),
	}, nil
}

// ensureDeployedApp refuses an update unless the Infra was deployed with appID.
//
// The check reads the deployment label Plan writes. An Infra without it was not
// deployed through this service, so nothing says which application it serves.
func (s *Service) ensureDeployedApp(ctx context.Context, nsID, infraID, appID string) error {
	infra, err := s.tumblebug.GetInfra(ctx, nsID, infraID)
	if err != nil {
		return err
	}
	deployed, ok := infra.Label[labelAppID]
	if !ok || deployed == "" {
		return fmt.Errorf("%w: %s has no %s label", ErrAppMismatch, infraID, labelAppID)
	}
	if deployed != appID {
		return fmt.Errorf("%w: %s serves %s, not %s", ErrAppMismatch, infraID, deployed, appID)
	}
	return nil
}

// infraRequestOf reads the CB-Tumblebug request a plan carries.
func infraRequestOf(plan *model.DeploymentPlan) (*tumblebug.InfraDynamicReq, error) {
	infraReq, ok := plan.Request.(*tumblebug.InfraDynamicReq)
	if !ok {
		return nil, fmt.Errorf("deployment plan for %q carries no provisioning request", plan.InfraName)
	}
	return infraReq, nil
}

// joinOrdered joins command outputs keyed by their decimal position, in order.
func joinOrdered(outputs map[string]string) string {
	keys := make([]string, 0, len(outputs))
	for key := range outputs {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, errA := strconv.Atoi(keys[i])
		b, errB := strconv.Atoi(keys[j])
		if errA != nil || errB != nil {
			return keys[i] < keys[j]
		}
		return a < b
	})

	var parts []string
	for _, key := range keys {
		if text := strings.TrimSpace(outputs[key]); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n")
}

// tail keeps the last limit bytes of text.
func tail(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return text[len(text)-limit:]
}

// Update applies a new version of an application to a running deployment.
func (s *Service) Update(ctx context.Context, nsID, infraID, appID string) (*model.ProvisionResult, error) {
	app, err := s.catalog.Get(ctx, appID)
	if err != nil {
		return nil, err
	}

	// The application check needs a CB-Tumblebug read, which a dry run does not
	// make. The result says so rather than implying the check passed.
	if s.dryRun {
		log.Warn().Str("nsId", nsID).Str("infraId", infraID).Str("appId", appID).
			Msg("Dry run: update prepared but not sent")
		return &model.ProvisionResult{
			InfraID:    infraID,
			AppID:      app.ID,
			AppVersion: app.Version,
			DryRun:     true,
			Message: fmt.Sprintf("Dry run: %s %s not applied to %s (app id not checked in dry run)",
				app.ID, app.Version, infraID),
		}, nil
	}

	if err := s.ensureDeployedApp(ctx, nsID, infraID, app.ID); err != nil {
		return nil, err
	}
	requirements, err := s.resolveRequirements(ctx, nsID, app)
	if err != nil {
		return nil, err
	}
	if len(requirements) > 0 {
		wired := *app
		wired.Install.Commands = requirementCommands(app.Install.Commands, requirements)
		app = &wired
	}

	result, err := s.provisioner.Update(ctx, nsID, infraID, app)
	if err != nil {
		return nil, err
	}

	log.Info().Str("nsId", nsID).Str("infraId", infraID).Str("appId", app.ID).
		Str("appVersion", app.Version).Int("nodes", len(result.Nodes)).Msg("Updated AI application")
	return result, nil
}
