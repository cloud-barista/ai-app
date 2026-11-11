package nodedeploy

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"strings"

	"github.com/ish-hcc/ai-agent-proto/internal/model"
)

func (s *Service) lockInferenceBinding(ctx context.Context, target model.NodeDeployTarget, id string) (func(), error) {
	d, ok := s.store.Get(id)
	if !ok || d.Type != model.NodeDeployTypeInference || d.Request.Inference == nil || d.DryRun {
		return nil, errors.New("the bound inference deployment does not exist")
	}
	place, err := placementFor(&d.Request)
	if err != nil {
		return nil, err
	}
	return s.locks.lock(ctx, target.Key()+" "+place.WorkKey)
}

func (s *Service) applicationEnv(ctx context.Context, target model.NodeDeployTarget, app *model.NodeAppReq) (map[string]string, error) {
	if len(app.EnvFrom) == 0 && app.InferenceBinding == nil {
		return app.Env, nil
	}
	env := make(map[string]string, len(app.Env)+len(app.EnvFrom)+2)
	maps.Copy(env, app.Env)
	for name, ref := range app.EnvFrom {
		value, ok := os.LookupEnv(ref)
		if !ok || value == "" {
			return nil, fmt.Errorf("app environment reference for %s is unavailable", name)
		}
		env[name] = value
	}
	if b := app.InferenceBinding; b != nil {
		if err := s.bindInference(ctx, target, b, env); err != nil {
			return nil, err
		}
	}
	return env, model.ValidateDeployEnv("app.env", env)
}

func (s *Service) bindInference(ctx context.Context, target model.NodeDeployTarget, binding *model.NodeInferenceBinding, env map[string]string) error {
	available := true
	filter := EndpointFilter{
		NsID: target.NsID, InfraID: target.InfraID, NodeID: target.NodeID,
		Type: model.NodeDeployTypeInference, Available: &available,
	}
	for _, endpoint := range s.Endpoints(ctx, filter) {
		if endpoint.DeploymentID != binding.DeploymentID || endpoint.URL == "" {
			continue
		}
		address := endpoint.URL
		if binding.Native {
			if endpoint.RuntimeID != model.RuntimeOllama {
				return errors.New("native connection requires Ollama")
			}
			address = strings.TrimSuffix(address, "/v1")
		}
		if binding.APIKeyEnv != "" {
			if endpoint.APIKey == "" {
				return errors.New("the bound inference deployment does not provide an API key")
			}
			env[binding.APIKeyEnv] = endpoint.APIKey
		}
		env[binding.BaseURLEnv] = address
		return nil
	}
	return errors.New("the bound inference deployment is not serving on this node")
}
