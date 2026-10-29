package model

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Deployment types of a node deployment request.
const (
	// NodeDeployTypeModelService runs an inference engine loading one model
	// artifact, from a compose file rendered by this service.
	NodeDeployTypeModelService = "model-service"
	// NodeDeployTypeApp copies an uploaded application version to the node and
	// runs its script.
	NodeDeployTypeApp = "app"
)

// Target kinds of a node deployment request.
const (
	// NodeTargetKindNode is a node of a CB-Tumblebug Infra.
	NodeTargetKindNode = "node"
	// NodeTargetKindK8s is a CB-Tumblebug Kubernetes cluster. The kind is
	// reserved and refused with 422.
	NodeTargetKindK8s = "k8s"
)

// Statuses of a node deployment and of each of its nodes.
const (
	// NodeDeployPending is a deployment accepted and not started yet.
	NodeDeployPending = "pending"
	// NodeDeployRunning is a deployment whose playbooks are running.
	NodeDeployRunning = "running"
	// NodeDeploySucceeded is a deployment whose every node succeeded.
	NodeDeploySucceeded = "succeeded"
	// NodeDeployFailed is a deployment whose every node failed.
	NodeDeployFailed = "failed"
	// NodeDeployPartial is a deployment where some nodes succeeded and some
	// failed. Only a deployment has it, never a node.
	NodeDeployPartial = "partial"
	// NodeDeployDryRun is a deployment that only placed its files, because the
	// service runs with dry run on. Nothing was registered in Semaphore.
	NodeDeployDryRun = "dry-run"
	// NodeDeployInterrupted is a deployment the service stopped following,
	// because it shut down or restarted while the deployment ran. What the
	// node ended up with is unknown.
	NodeDeployInterrupted = "interrupted"
)

// MaxNodeDeployTargets bounds the targets of one deployment request.
const MaxNodeDeployTargets = 32

// nodeTargetIDPattern is what a namespace, Infra or node identifier may look
// like. Identifiers become a folder name on the shared stage volume.
var nodeTargetIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// ErrUnsupportedTarget marks a target kind that is reserved but not deployed
// to yet.
var ErrUnsupportedTarget = errors.New("unsupported deployment target")

// NodeDeployTarget is one place a deployment goes to, named as CB-Tumblebug
// names it. The cloud, region and zone are read from the node, not given.
type NodeDeployTarget struct {
	// Kind is "node". "k8s" is reserved for Kubernetes clusters and refused
	// with 422.
	Kind string `json:"kind" validate:"required" enums:"node,k8s" example:"node"`
	// NsID is the CB-Tumblebug namespace of the Infra.
	NsID string `json:"nsId" validate:"required" example:"etri"`
	// InfraID is the CB-Tumblebug Infra the node belongs to.
	InfraID string `json:"infraId,omitempty" example:"travel"`
	// NodeID is the node within the Infra.
	NodeID string `json:"nodeId,omitempty" example:"vm1"`
	// K8sClusterID is reserved for kind "k8s".
	K8sClusterID string `json:"k8sClusterId,omitempty" example:""`
	// NodeGroupID is reserved for kind "k8s".
	NodeGroupID string `json:"nodeGroupId,omitempty" example:""`
}

// Key identifies the target within one request.
func (t *NodeDeployTarget) Key() string {
	return t.NsID + "/" + t.InfraID + "/" + t.NodeID
}

// NodeModelServiceReq says which model artifact to serve and with which engine.
type NodeModelServiceReq struct {
	// ModelID is a model of the model catalog (GET /models).
	ModelID string `json:"modelId" validate:"required" example:"qwen3-embedding-0.6b"`
	// ArtifactID is an artifact of that model.
	ArtifactID string `json:"artifactId" validate:"required" example:"hf-bf16"`
	// Engine is an engine profile ID (GET /engines) the artifact lists. Empty
	// takes the first engine the artifact lists.
	Engine string `json:"engine,omitempty" example:"vllm"`
	// GPU reserves every NVIDIA GPU of the node for the engine and installs
	// the NVIDIA Container Toolkit. Omitted, it is true when the node spec in
	// CB-Tumblebug has acceleratorType gpu, and false when the spec cannot be
	// read; the node result says which.
	GPU *bool `json:"gpu,omitempty" example:"false"`
	// Env is added to the engine container environment and written to the
	// .env file beside the compose file. Names match [A-Z_][A-Z0-9_]*;
	// values hold no line break and no single quote.
	Env map[string]string `json:"env,omitempty"`
}

// NodeAppReq says which uploaded application version to deploy.
type NodeAppReq struct {
	// Name is an uploaded application (GET /apps).
	Name string `json:"name" validate:"required" example:"open-webui"`
	// Version is the version to deploy. 0 takes the latest; the record keeps
	// the version taken.
	Version int `json:"version,omitempty" example:"1"`
	// Env is written to the .env file in the folder the script runs in, and
	// exported before the script starts. Names match [A-Z_][A-Z0-9_]*; values
	// hold no line break and no single quote. Addresses of other nodes go
	// here.
	Env map[string]string `json:"env,omitempty"`
}

// NodeDeploymentReq deploys a model service or an application onto existing
// CB-Tumblebug nodes.
type NodeDeploymentReq struct {
	// Type is "model-service" or "app"; the field of the same name below
	// carries what to deploy.
	Type string `json:"type" validate:"required" enums:"model-service,app" example:"model-service"`
	// Targets are the nodes to deploy to, at most 32, each once.
	Targets []NodeDeployTarget `json:"targets" validate:"required"`
	// ModelService is required when type is model-service.
	ModelService *NodeModelServiceReq `json:"modelService,omitempty"`
	// App is required when type is app.
	App *NodeAppReq `json:"app,omitempty"`
}

// Validate reports why a request cannot be deployed. A k8s target fails with
// an error matching ErrUnsupportedTarget; every other problem is a
// ValidationError.
func (r *NodeDeploymentReq) Validate() error {
	switch r.Type {
	case NodeDeployTypeModelService:
		if r.ModelService == nil {
			return errRequired("modelService")
		}
		if r.App != nil {
			return errInvalid("app", "must be empty when type is model-service")
		}
		if r.ModelService.ModelID == "" {
			return errRequired("modelService.modelId")
		}
		if r.ModelService.ArtifactID == "" {
			return errRequired("modelService.artifactId")
		}
		if err := ValidateDeployEnv("modelService.env", r.ModelService.Env); err != nil {
			return err
		}
	case NodeDeployTypeApp:
		if r.App == nil {
			return errRequired("app")
		}
		if r.ModelService != nil {
			return errInvalid("modelService", "must be empty when type is app")
		}
		if err := ValidateAppName(r.App.Name); err != nil {
			return &ValidationError{Message: "app." + ValidationMessage(err)}
		}
		if r.App.Version < 0 {
			return errInvalid("app.version", "must be 0 (latest) or a version number")
		}
		if err := ValidateDeployEnv("app.env", r.App.Env); err != nil {
			return err
		}
	case "":
		return errRequired("type")
	default:
		return errInvalid("type", "must be model-service or app")
	}

	if len(r.Targets) == 0 {
		return errRequired("targets")
	}
	if len(r.Targets) > MaxNodeDeployTargets {
		return errInvalid("targets", fmt.Sprintf("must hold at most %d targets", MaxNodeDeployTargets))
	}
	seen := make(map[string]bool, len(r.Targets))
	for i := range r.Targets {
		t := &r.Targets[i]
		field := fmt.Sprintf("targets[%d]", i)
		switch t.Kind {
		case NodeTargetKindNode:
		case NodeTargetKindK8s:
			return fmt.Errorf("%w: %s: kind k8s is not deployed to yet; use kind node", ErrUnsupportedTarget, field)
		case "":
			return errRequired(field + ".kind")
		default:
			return errInvalid(field+".kind", "must be node or k8s")
		}
		for _, id := range []struct{ name, value string }{
			{"nsId", t.NsID}, {"infraId", t.InfraID}, {"nodeId", t.NodeID},
		} {
			if id.value == "" {
				return errRequired(field + "." + id.name)
			}
			if !nodeTargetIDPattern.MatchString(id.value) {
				return errInvalid(field+"."+id.name, "must match [A-Za-z0-9][A-Za-z0-9._-]* and be at most 128 characters")
			}
		}
		if seen[t.Key()] {
			return errInvalid(field, "names a node that is already a target")
		}
		seen[t.Key()] = true
	}
	return nil
}

// ValidateDeployEnv reports why env cannot be written to a .env file. The file
// is read by docker compose and sourced by bash, and each value is written in
// single quotes, which both read literally.
func ValidateDeployEnv(field string, env map[string]string) error {
	for name, value := range env {
		if !envNamePattern.MatchString(name) {
			return errInvalid(field, fmt.Sprintf("name %q must match [A-Z_][A-Z0-9_]*", name))
		}
		if strings.ContainsAny(value, "\r\n") {
			return errInvalid(field, fmt.Sprintf("value of %s must not hold a line break", name))
		}
		if strings.Contains(value, "'") {
			return errInvalid(field, fmt.Sprintf("value of %s must not hold a single quote", name))
		}
	}
	return nil
}

// NodeDeployment is the record of one deployment request.
type NodeDeployment struct {
	// ID is a UUID (version 4). It names the stage folder and, on each node,
	// /opt/aiapp/<id>.
	ID string `json:"id" example:"3f9b2c4e-8a51-4d27-9e0c-5b1f6a7d2e48"`
	// Type is the request type.
	Type string `json:"type" enums:"model-service,app" example:"model-service"`
	// Status is pending, running, succeeded (every node), failed (every node),
	// partial (some nodes), dry-run (files placed, nothing run) or interrupted
	// (the service stopped while it ran).
	Status string `json:"status" enums:"pending,running,succeeded,failed,partial,dry-run,interrupted" example:"succeeded"`
	// DryRun is true when the service ran with dry run on: only the node
	// look-up and file placement were done.
	DryRun bool `json:"dryRun" example:"false"`
	// Request is the request as accepted, with the engine and app version
	// that were taken filled in.
	Request NodeDeploymentReq `json:"request"`
	// Nodes are the per node results, in request order.
	Nodes []NodeDeployResult `json:"nodes"`
	// Error is why the deployment as a whole stopped, when it did.
	Error string `json:"error,omitempty"`
	// CreatedAt is when the request was accepted.
	CreatedAt time.Time `json:"createdAt" example:"2026-10-29T12:00:00Z"`
	// FinishedAt is when the last node finished. Absent while it runs.
	FinishedAt *time.Time `json:"finishedAt,omitempty" example:"2026-10-29T12:05:00Z"`
}

// NodeDeployResult is what happened on one node.
type NodeDeployResult struct {
	// Target is the target as requested.
	Target NodeDeployTarget `json:"target"`
	// Status is pending, running, succeeded, failed, dry-run or interrupted.
	Status string `json:"status" enums:"pending,running,succeeded,failed,dry-run,interrupted" example:"succeeded"`
	// Node is how the node was reached, as read from CB-Tumblebug. Absent
	// when the node could not be read. The SSH key is never included.
	Node *NodeDeployNode `json:"node,omitempty"`
	// GPU is whether the engine was given the node's GPUs and the NVIDIA
	// Container Toolkit was installed.
	GPU bool `json:"gpu" example:"false"`
	// GPUSource says where GPU came from: request, spec, or default when the
	// spec could not be read.
	GPUSource string `json:"gpuSource,omitempty" enums:"request,spec,default" example:"spec"`
	// Notes record decisions made for this node, such as why the spec could
	// not be read.
	Notes []string `json:"notes,omitempty"`
	// StageDir is the folder the files were placed in on the shared stage
	// volume: <AIAPP_STAGE_ROOT>/<deployment id>/<csp>-<node id>.
	StageDir string `json:"stageDir,omitempty" example:"/var/lib/aiapp/stage/3f9b2c4e-8a51-4d27-9e0c-5b1f6a7d2e48/aws-vm1"`
	// Compose is the rendered docker-compose.yml of a model service.
	Compose string `json:"compose,omitempty"`
	// ServiceURL is the model service API on the node, built from the public
	// or private IP the node was reached at, the engine port and its API base
	// path. Model services only.
	ServiceURL string `json:"serviceUrl,omitempty" example:"http://203.0.113.10:8000/v1"`
	// Steps are the playbooks run on the node, in order. A step that failed
	// ends the node.
	Steps []NodeDeployStep `json:"steps,omitempty"`
	// Error is why the node failed.
	Error string `json:"error,omitempty"`
	// StartedAt is when work on the node began.
	StartedAt *time.Time `json:"startedAt,omitempty" example:"2026-10-29T12:00:01Z"`
	// FinishedAt is when work on the node ended.
	FinishedAt *time.Time `json:"finishedAt,omitempty" example:"2026-10-29T12:04:59Z"`
}

// NodeDeployNode is how a node was reached. The SSH key is never part of it.
type NodeDeployNode struct {
	// CSP is the cloud, from connectionConfig.providerName, in lower case.
	CSP string `json:"csp" example:"aws"`
	// Region and Zone are where the node runs.
	Region string `json:"region,omitempty" example:"ap-northeast-2"`
	Zone   string `json:"zone,omitempty" example:"ap-northeast-2a"`
	// Host is the public IP, or the private IP when the node has none.
	Host string `json:"host" example:"203.0.113.10"`
	// Port is the SSH port.
	Port int `json:"port" example:"22"`
	// User is the SSH user.
	User string `json:"user" example:"cb-user"`
	// SpecID is the node spec in CB-Tumblebug.
	SpecID string `json:"specId,omitempty" example:"aws+ap-northeast-2+g4dn.xlarge"`
}

// NodeDeployStep is one playbook run through Semaphore.
type NodeDeployStep struct {
	// Playbook is install-docker.yml, compose-up.yml or copy-and-run.yml.
	Playbook string `json:"playbook" example:"compose-up.yml"`
	// TaskID is the Semaphore task, kept in Semaphore with its log.
	TaskID int `json:"taskId,omitempty" example:"2147483645"`
	// Status is the final Semaphore task status: success, error, stopped or
	// rejected, or empty when the task never started.
	Status string `json:"status" example:"success"`
	// Output is the task log, one line per entry. It is returned by
	// GET /deployments/{id}/logs and left out of the deployment record.
	Output []string `json:"output,omitempty"`
	// CleanupErrors name the Semaphore key or inventory of this run that
	// could not be deleted. A key named here may still be stored in Semaphore.
	CleanupErrors []string `json:"cleanupErrors,omitempty"`
	// Error is why the run could not be registered, started or followed.
	Error      string     `json:"error,omitempty"`
	StartedAt  time.Time  `json:"startedAt" example:"2026-10-29T12:00:02Z"`
	FinishedAt *time.Time `json:"finishedAt,omitempty" example:"2026-10-29T12:02:10Z"`
}

// NodeDeploymentListResp is the node deployment listing body.
type NodeDeploymentListResp struct {
	// Deployments are ordered newest first, without task output.
	Deployments []NodeDeployment `json:"deployments"`
}

// NodeDeploymentLogs is the task output of one deployment.
type NodeDeploymentLogs struct {
	ID     string `json:"id" example:"3f9b2c4e-8a51-4d27-9e0c-5b1f6a7d2e48"`
	Status string `json:"status" example:"succeeded"`
	// Nodes hold, per target, the steps with their output.
	Nodes []NodeDeployNodeLogs `json:"nodes"`
}

// NodeDeployNodeLogs is the task output of one node.
type NodeDeployNodeLogs struct {
	Target NodeDeployTarget `json:"target"`
	Status string           `json:"status" example:"succeeded"`
	Steps  []NodeDeployStep `json:"steps"`
}

// NodeControlReq applies a lifecycle action to every node of a deployment.
type NodeControlReq struct {
	// Action is passed to CB-Tumblebug node control.
	Action string `json:"action" validate:"required" enums:"suspend,resume,reboot,terminate" example:"reboot"`
}

// NodeControlResp is the result of a control request.
type NodeControlResp struct {
	ID     string `json:"id" example:"3f9b2c4e-8a51-4d27-9e0c-5b1f6a7d2e48"`
	Action string `json:"action" example:"reboot"`
	// Nodes are the per node results, in deployment order.
	Nodes []NodeControlResult `json:"nodes"`
}

// NodeControlResult is the control result of one node.
type NodeControlResult struct {
	Target NodeDeployTarget `json:"target"`
	// Status is done, failed, or blocked when nothing was sent because the
	// service or the deployment is in dry run.
	Status string `json:"status" enums:"done,failed,blocked" example:"done"`
	// Message is the CB-Tumblebug answer, or why nothing was sent.
	Message string `json:"message,omitempty" example:"Rebooting the node vm1"`
}
