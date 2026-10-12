package deploy

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/rs/zerolog/log"

	"github.com/ish-hcc/ai-agent-proto/internal/model"
	"github.com/ish-hcc/ai-agent-proto/internal/tumblebug"
)

// statusRunning is the CB-Tumblebug status word of a serving Infra or node. An
// Infra status carries counts after it ("Running:1 (R:1/1)"), so it is matched
// as a substring there.
const statusRunning = "Running"

// ErrRequirementNotDeployed reports that an application names a model service
// that has no running deployment in the namespace.
var ErrRequirementNotDeployed = errors.New("required model service is not deployed")

// RequirementError names the model service that has to be deployed first.
//
// Its text is written for the caller, agent or operator, because the useful
// answer to it is to deploy that model service and try again.
type RequirementError struct {
	AppID         string
	RequiredAppID string
	Namespace     string
}

func (e *RequirementError) Error() string {
	return fmt.Sprintf("application %s needs model service %s deployed in namespace %s first",
		e.AppID, e.RequiredAppID, e.Namespace)
}

// Is lets errors.Is(err, ErrRequirementNotDeployed) match any RequirementError.
func (e *RequirementError) Is(target error) bool { return target == ErrRequirementNotDeployed }

// resolveRequirements finds a running deployment of every model service an
// application requires and renders the endpoint it is reached at.
//
// A model service needs nothing and resolves to no requirements.
func (s *Service) resolveRequirements(ctx context.Context, nsID string, app *model.AppSpec) ([]model.ResolvedRequirement, error) {
	if app.EffectiveKind() != model.KindApplication || len(app.Requires) == 0 {
		return nil, nil
	}

	infras, err := s.tumblebug.ListInfra(ctx, nsID)
	if err != nil {
		return nil, err
	}

	resolved := make([]model.ResolvedRequirement, 0, len(app.Requires))
	for _, req := range app.Requires {
		required, err := s.catalog.Get(ctx, req.AppID)
		if err != nil {
			return nil, fmt.Errorf("model service %s required by %s: %w", req.AppID, app.ID, err)
		}
		infra, node := runningDeployment(infras, req.AppID)
		if infra == nil {
			return nil, &RequirementError{AppID: app.ID, RequiredAppID: req.AppID, Namespace: nsID}
		}
		resolved = append(resolved, model.ResolvedRequirement{
			AppID:   req.AppID,
			Env:     req.Env,
			InfraID: infra.ID,
			Endpoint: fmt.Sprintf("%s://%s:%d%s",
				servingProtocol(required), node.PublicIP, required.Serving.Port, required.Serving.APIBasePath),
		})
	}

	log.Info().Str("nsId", nsID).Str("appId", app.ID).Int("requirements", len(resolved)).
		Msg("Resolved required model services")
	return resolved, nil
}

// runningDeployment picks the first running Infra of an application that has a
// running node with a public address, and that node.
func runningDeployment(infras []tumblebug.InfraInfo, appID string) (*tumblebug.InfraInfo, *tumblebug.NodeInfo) {
	for i := range infras {
		infra := &infras[i]
		if infra.Label[labelAppID] != appID || !strings.Contains(infra.Status, statusRunning) {
			continue
		}
		for j := range infra.Node {
			node := &infra.Node[j]
			if node.PublicIP != "" && (node.Status == "" || node.Status == statusRunning) {
				return infra, node
			}
		}
	}
	return nil, nil
}

// requirementCommands returns the install commands of an application with the
// resolved endpoints exported in front of every command.
//
// The export goes on every command rather than on a line of its own because
// CB-Tumblebug opens a new SSH session per command (remote_command_ssh.go in
// v0.13.3), so a variable exported by one command is gone by the next.
func requirementCommands(commands []string, resolved []model.ResolvedRequirement) []string {
	if len(resolved) == 0 {
		return commands
	}
	assignments := make([]string, 0, len(resolved))
	for _, r := range resolved {
		assignments = append(assignments, r.Env+"="+shellQuote(r.Endpoint))
	}
	prefix := "export " + strings.Join(assignments, " ") + " && "

	out := make([]string, 0, len(commands))
	for _, command := range commands {
		out = append(out, prefix+command)
	}
	return out
}

// shellQuote leaves a word of plain URL characters as it is and single quotes
// anything else, so an endpoint never turns into shell syntax.
func shellQuote(value string) string {
	plain := value != ""
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("_-./:@%+=,", r)) {
			plain = false
			break
		}
	}
	if plain {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}
