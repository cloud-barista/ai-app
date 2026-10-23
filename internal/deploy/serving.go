package deploy

import (
	"context"
	"fmt"
	"strconv"

	"github.com/rs/zerolog/log"

	"github.com/ish-hcc/ai-agent-proto/internal/model"
	"github.com/ish-hcc/ai-agent-proto/internal/tumblebug"
)

// anyIPv4 is the source range the serving port is opened to.
//
// A deployment created from a catalog entry has no caller list to narrow this
// with yet. Restricting it belongs with the access policy work, and until then
// the open range is reported back rather than left implicit.
const anyIPv4 = "0.0.0.0/0"

// OpenServingPort adds an inbound rule for the application's serving port.
//
// Provisioning opens SSH only, so without this the application answers on a port
// nothing can reach: the deployment looks finished and the endpoint is dead. The
// rule is added after creation because the security group is created as part of
// provisioning and its identifier is only known once the nodes exist.
func (s *Service) OpenServingPort(ctx context.Context, nsID string, app *model.AppSpec, infra *tumblebug.InfraInfo) (*model.ServingAccess, error) {
	access := &model.ServingAccess{
		Port:     app.Serving.Port,
		Protocol: servingProtocol(app),
		SourceIP: anyIPv4,
	}

	groups := securityGroupIDs(infra)
	if len(groups) == 0 {
		access.Message = "No security group is attached to the deployment, so the serving port could not be opened"
		log.Warn().Str("nsId", nsID).Str("infraId", infra.ID).
			Msg("Deployment has no security group, serving port left closed")
		return access, nil
	}

	rules := []tumblebug.FirewallRuleReq{{
		Direction: tumblebug.DirectionInbound,
		Protocol:  tumblebug.ProtocolTCP,
		Ports:     strconv.Itoa(app.Serving.Port),
		CIDR:      anyIPv4,
	}}

	for _, groupID := range groups {
		if _, err := s.tumblebug.AddFirewallRules(ctx, nsID, groupID, rules); err != nil {
			// The nodes are already running and billing. Reporting a closed port
			// is more useful than discarding a finished deployment, so the error
			// is carried in the result instead of aborting.
			access.Message = fmt.Sprintf("Serving port %d could not be opened; open it manually before using the endpoint", app.Serving.Port)
			log.Error().Err(err).Str("nsId", nsID).Str("infraId", infra.ID).
				Str("securityGroupId", groupID).Int("port", app.Serving.Port).
				Msg("Failed to open serving port")
			return access, nil
		}
		access.SecurityGroupIDs = append(access.SecurityGroupIDs, groupID)
	}

	access.Opened = true
	access.Endpoints = servingEndpoints(app, infra)
	access.Message = fmt.Sprintf("Serving port %d opened to %s", app.Serving.Port, anyIPv4)

	log.Info().Str("nsId", nsID).Str("infraId", infra.ID).Int("port", app.Serving.Port).
		Strs("securityGroupIds", access.SecurityGroupIDs).Msg("Opened serving port")

	return access, nil
}

// securityGroupIDs collects the distinct security groups of a deployment's nodes.
func securityGroupIDs(infra *tumblebug.InfraInfo) []string {
	seen := make(map[string]bool)
	groups := make([]string, 0, 1)
	for _, node := range infra.Node {
		for _, groupID := range node.SecurityGroupIDs {
			if groupID == "" || seen[groupID] {
				continue
			}
			seen[groupID] = true
			groups = append(groups, groupID)
		}
	}
	return groups
}

// servingEndpoints renders the reachable address of every node that has one.
func servingEndpoints(app *model.AppSpec, infra *tumblebug.InfraInfo) []string {
	endpoints := make([]string, 0, len(infra.Node))
	for _, node := range infra.Node {
		if node.PublicIP == "" {
			continue
		}
		endpoints = append(endpoints, fmt.Sprintf("%s://%s:%d%s",
			servingProtocol(app), node.PublicIP, app.Serving.Port, app.Serving.HealthPath))
	}
	return endpoints
}

func servingProtocol(app *model.AppSpec) string {
	if app.Serving.Protocol == "" {
		return "http"
	}
	return app.Serving.Protocol
}
