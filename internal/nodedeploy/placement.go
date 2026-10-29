package nodedeploy

import (
	"context"
	"fmt"
	"path"
	"regexp"
	"sync"

	"github.com/ish-hcc/ai-agent-proto/internal/model"
)

// NodeRoot is the folder on a node that holds one folder per work key.
const NodeRoot = "/opt/aiapp"

// Prefixes of a work key, and of the compose project named after it.
const (
	workKeyModelService = "ms-"
	workKeyApp          = "app-"
	composeProjectBase  = "aiapp-"
)

// composeProjectPattern is what docker compose accepts as a project name.
var composeProjectPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// placement is where a deployment lands on a node. A node holds one model
// service per engine and one copy of each application: deploying the same
// work key again replaces what the previous deployment left there, in the same
// folder and the same compose project, so that the old containers give up
// their ports instead of keeping them.
type placement struct {
	// WorkKey is ms-<engine> for a model service and app-<name> for an
	// application.
	WorkKey string
	// NodeDir is <NodeRoot>/<work key> on the node.
	NodeDir string
	// Project is the compose project name, aiapp-<work key>.
	Project string
}

// placementFor works out the placement of a resolved request. It refuses a
// work key that docker compose would not take as part of a project name.
func placementFor(req *model.NodeDeploymentReq) (placement, error) {
	var key string
	switch req.Type {
	case model.NodeDeployTypeModelService:
		key = workKeyModelService + req.ModelService.Engine
	case model.NodeDeployTypeApp:
		key = workKeyApp + req.App.Name
	default:
		return placement{}, &model.ValidationError{Message: fmt.Sprintf("type %q has no place on a node", req.Type)}
	}
	project := composeProjectBase + key
	if !composeProjectPattern.MatchString(project) {
		return placement{}, &model.ValidationError{Message: fmt.Sprintf(
			"work key %q cannot name a docker compose project: only lower case letters, digits, \"_\" and \"-\" are allowed", key)}
	}
	return placement{WorkKey: key, NodeDir: path.Join(NodeRoot, key), Project: project}, nil
}

// placementLocks lets one deployment at a time work on a placement of a node,
// so that two deployments of the same work key do not replace each other's
// files halfway.
type placementLocks struct {
	mu    sync.Mutex
	locks map[string]chan struct{}
}

// lock waits until the placement key of a node is free, or ctx ends. The
// returned function frees it.
func (l *placementLocks) lock(ctx context.Context, key string) (func(), error) {
	l.mu.Lock()
	if l.locks == nil {
		l.locks = make(map[string]chan struct{})
	}
	ch, ok := l.locks[key]
	if !ok {
		ch = make(chan struct{}, 1)
		l.locks[key] = ch
	}
	l.mu.Unlock()
	select {
	case ch <- struct{}{}:
		return func() { <-ch }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
