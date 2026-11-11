package tools

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/ish-hcc/ai-agent-proto/internal/model"
	"github.com/ish-hcc/ai-agent-proto/internal/nodedeploy"
)

// NewApprovedNodeDeployRegistry exposes the backend of the approved engine only.
func NewApprovedNodeDeployRegistry(svc *nodedeploy.Service) *Registry {
	registry := NewRegistryFunc(svc.DryRun)
	tool := createDeploymentTool(svc)
	tool.Description = "Submit the approved deployment for this namespace using its reserved submission ID."
	tool.Handle = func(ctx context.Context, nsID string, input json.RawMessage) (any, error) {
		var req model.NodeDeploymentReq
		if err := decode(input, &req); err != nil {
			return nil, err
		}
		if len(req.Targets) != 1 || req.Targets[0].NsID != nsID {
			return nil, errors.New("approved submission requires one node in the request namespace")
		}
		return svc.CreateApproved(ctx, req)
	}
	registry.Add(tool)
	return registry
}
