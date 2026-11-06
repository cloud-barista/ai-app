package model

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Deployment types of a node deployment request.
const (
	// NodeDeployTypeInference runs an inference runtime loading one model
	// artifact, from a compose file rendered by this service.
	NodeDeployTypeInference = "inference"
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
	// NodeDeployDeploying is a deployment whose playbooks are running.
	NodeDeployDeploying = "deploying"
	// NodeDeployRunning is a deployment whose every node was deployed: its
	// playbooks succeeded and the service runs there.
	NodeDeployRunning = "running"
	// NodeDeployFailed is a deployment whose every node failed.
	NodeDeployFailed = "failed"
	// NodeDeployPartial is a deployment where some nodes run and some
	// failed. Only a deployment has it, never a node.
	NodeDeployPartial = "partial"
	// NodeDeployDryRun is a deployment that only placed its files, because the
	// service runs with dry run on. Nothing was registered in Semaphore.
	NodeDeployDryRun = "dry-run"
	// NodeDeployInterrupted is a deployment the service stopped following,
	// because it shut down or restarted while the deployment ran. What the
	// node ended up with is unknown.
	NodeDeployInterrupted = "interrupted"
	// NodeDeployRemoving is a deployment being taken down from its nodes by
	// DELETE /deployments/{id}: compose-down runs on the nodes that need it.
	NodeDeployRemoving = "removing"
	// NodeDeployRemoved is a deployment taken down from every node. Its
	// containers are gone; its node folders and named volumes are kept. A
	// node is also removed without running anything when the deployment was
	// a dry run, never started its service there, or was replaced there by a
	// later deployment of the same work key; its notes say which.
	NodeDeployRemoved = "removed"
	// NodeDeployRemoveFailed is a deployment whose compose-down failed on at
	// least one node. Removing it again retries the nodes not removed yet.
	NodeDeployRemoveFailed = "remove-failed"
)

// MaxNodeDeployTargets bounds the targets of one deployment request.
const MaxNodeDeployTargets = 32

// nodeTargetIDPattern is what a namespace, Infra or node identifier may look
// like. Identifiers become a folder name on the shared stage volume.
var nodeTargetIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// sshUserPattern is what a target SSH user may look like: a Linux user name of
// 1 to 32 characters.
var sshUserPattern = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

// apiKeyPattern is what an inference API key given in a request may look
// like: 16 to 128 characters a bearer token, a .env file and a YAML scalar
// all carry as they are.
var apiKeyPattern = regexp.MustCompile(`^[A-Za-z0-9._~-]{16,128}$`)

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
	// SSHUser replaces, for this node, the SSH user CB-Tumblebug reports
	// (nodeUserName) in the Semaphore inventory; the SSH key is still the one
	// CB-Tumblebug holds for the node. Without it, the CB-Tumblebug user is
	// tried with the key before the playbooks run, and when the node refuses
	// it (CB-Tumblebug could not create cb-user on the image), the image's
	// default users are tried. Give it to choose the user, and no login is
	// tried. 1 to 32 characters matching [a-z_][a-z0-9_-]*. Taking the
	// deployment down uses the user recorded at deploy time.
	SSHUser string `json:"sshUser,omitempty" example:"ec2-user"`
	// K8sClusterID is reserved for kind "k8s".
	K8sClusterID string `json:"k8sClusterId,omitempty" example:""`
	// NodeGroupID is reserved for kind "k8s".
	NodeGroupID string `json:"nodeGroupId,omitempty" example:""`
}

// Key identifies the target within one request.
func (t *NodeDeployTarget) Key() string {
	return t.NsID + "/" + t.InfraID + "/" + t.NodeID
}

// NodeInferenceReq says which model artifact to serve and with which runtime.
type NodeInferenceReq struct {
	// ModelID is a model of the model catalog (GET /models).
	ModelID string `json:"modelId" validate:"required" example:"qwen3-embedding-0.6b"`
	// ArtifactID is an artifact of that model.
	ArtifactID string `json:"artifactId" validate:"required" example:"hf-bf16"`
	// RuntimeID is a runtime profile ID (GET /runtimes) the artifact lists.
	// Empty takes the first runtime the artifact lists.
	RuntimeID string `json:"runtimeId,omitempty" example:"vllm"`
	// GPU reserves every NVIDIA GPU of the node for the runtime and installs
	// the NVIDIA Container Toolkit. Omitted, it is true when the node spec in
	// CB-Tumblebug has acceleratorType gpu, and false when the spec cannot be
	// read; the node result says which.
	GPU *bool `json:"gpu,omitempty" example:"false"`
	// Env is added to the runtime container environment and written to the
	// .env file beside the compose file. Names match [A-Z_][A-Z0-9_]*;
	// values hold no line break and no single quote.
	Env map[string]string `json:"env,omitempty"`
	// Args are appended to the runtime command after the arguments this
	// service sets and the catalog arguments of the artifact
	// (runtimes[].args). For vllm they follow `vllm serve --model <ref>
	// --port <port>` (and --runner pooling for an embedding model) and the
	// catalog arguments, for example ["--max-model-len", "32768"]; vllm takes
	// the last value of a repeated flag, so they override the catalog
	// arguments. At most 32, each 1 to 256 characters
	// without control characters; --model, --port and --host are set by the
	// service and refused. Runtime ollama takes none and refuses them.
	Args []string `json:"args,omitempty" example:"--max-model-len,32768"`
	// APIKey is the key callers of a vllm service send as
	// `Authorization: Bearer <apiKey>` on /v1 paths; /health stays open. It
	// is 16 to 128 characters of [A-Za-z0-9._~-]. Omitted, each node keeps
	// the key of its previous deployment of the same work key, or gets a
	// new random one. The key is given to vLLM as the VLLM_API_KEY
	// environment variable and shown in the node results and on
	// GET /endpoints. Runtime ollama has no key and refuses one.
	APIKey string `json:"apiKey,omitempty" example:"3f1c9a7e5b2d4f6a8c0e1b3d5f7a9c2e"`
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
	// Ports replace, for this deployment, the ports the application version
	// was uploaded with: the TCP ports opened inbound in the node's
	// CB-Tumblebug security groups, each from 1 to 65535, at most 16.
	// Omitted takes the version's ports.
	Ports []int `json:"ports,omitempty" example:"3000"`
}

// NodeDeploymentReq deploys an inference service or an application onto
// existing CB-Tumblebug nodes.
type NodeDeploymentReq struct {
	// Type is "inference" or "app"; the field of the same name below carries
	// what to deploy.
	Type string `json:"type" validate:"required" enums:"inference,app" example:"inference"`
	// Targets are the nodes to deploy to, at most 32, each once.
	Targets []NodeDeployTarget `json:"targets" validate:"required"`
	// Inference is required when type is inference.
	Inference *NodeInferenceReq `json:"inference,omitempty"`
	// App is required when type is app.
	App *NodeAppReq `json:"app,omitempty"`
}

// Validate reports why a request cannot be deployed. A k8s target fails with
// an error matching ErrUnsupportedTarget; every other problem is a
// ValidationError.
func (r *NodeDeploymentReq) Validate() error {
	switch r.Type {
	case NodeDeployTypeInference:
		if r.Inference == nil {
			return errRequired("inference")
		}
		if r.App != nil {
			return errInvalid("app", "must be empty when type is inference")
		}
		if r.Inference.ModelID == "" {
			return errRequired("inference.modelId")
		}
		if r.Inference.ArtifactID == "" {
			return errRequired("inference.artifactId")
		}
		if err := ValidateDeployEnv("inference.env", r.Inference.Env); err != nil {
			return err
		}
		if err := ValidateRuntimeArgs("inference.args", r.Inference.Args); err != nil {
			return err
		}
		if r.Inference.APIKey != "" && !apiKeyPattern.MatchString(r.Inference.APIKey) {
			return errInvalid("inference.apiKey", "must be 16 to 128 characters of [A-Za-z0-9._~-]")
		}
	case NodeDeployTypeApp:
		if r.App == nil {
			return errRequired("app")
		}
		if r.Inference != nil {
			return errInvalid("inference", "must be empty when type is app")
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
		if err := ValidateAppPorts("app.ports", r.App.Ports); err != nil {
			return err
		}
	case "":
		return errRequired("type")
	default:
		return errInvalid("type", "must be inference or app")
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
		if t.SSHUser != "" && !sshUserPattern.MatchString(t.SSHUser) {
			return errInvalid(field+".sshUser", "must match [a-z_][a-z0-9_-]* and be at most 32 characters")
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

// MaxRuntimeArgs bounds the extra runtime arguments of one inference request.
const MaxRuntimeArgs = 32

// maxRuntimeArgLen bounds one extra runtime argument, in characters.
const maxRuntimeArgLen = 256

// builtinRuntimeFlags are the runtime options the rendered command already
// sets. The runtime parses its options with argparse, which takes a unique
// prefix of an option name for the option, so a prefix is refused as well.
var builtinRuntimeFlags = []string{"model", "port", "host"}

// ValidateRuntimeArgs reports why args cannot be appended to the runtime
// command of a compose file.
func ValidateRuntimeArgs(field string, args []string) error {
	if len(args) > MaxRuntimeArgs {
		return errInvalid(field, fmt.Sprintf("must hold at most %d arguments", MaxRuntimeArgs))
	}
	for i, arg := range args {
		item := fmt.Sprintf("%s[%d]", field, i)
		if n := utf8.RuneCountInString(arg); n == 0 || n > maxRuntimeArgLen {
			return errInvalid(item, fmt.Sprintf("must be 1 to %d characters", maxRuntimeArgLen))
		}
		if !utf8.ValidString(arg) || strings.IndexFunc(arg, unicode.IsControl) >= 0 {
			return errInvalid(item, "must not hold a control character or a line break")
		}
		if arg == "--" {
			return errInvalid(item, "must not be --")
		}
		if !strings.HasPrefix(arg, "--") {
			continue
		}
		name, _, _ := strings.Cut(strings.TrimPrefix(arg, "--"), "=")
		for _, builtin := range builtinRuntimeFlags {
			if strings.HasPrefix(builtin, name) {
				return errInvalid(item, fmt.Sprintf("must not set --%s, which the service sets", builtin))
			}
		}
	}
	return nil
}

// NodeDeployment is the record of one deployment request.
type NodeDeployment struct {
	// ID is a UUID (version 4). It names the stage folder. The folder on each
	// node is named after the work key instead; see NodeDeployResult.NodeDir.
	ID string `json:"id" example:"3f9b2c4e-8a51-4d27-9e0c-5b1f6a7d2e48"`
	// Type is the request type.
	Type string `json:"type" enums:"inference,app" example:"inference"`
	// Status is pending, deploying, running (every node), failed (every
	// node), partial (some nodes), dry-run (files placed, nothing run) or
	// interrupted (the service stopped while it was deploying); once taken
	// down with DELETE /deployments/{id}, removing, removed or remove-failed.
	Status string `json:"status" enums:"pending,deploying,running,failed,partial,dry-run,interrupted,removing,removed,remove-failed" example:"running"`
	// DryRun is true when the service ran with dry run on: only the node
	// look-up and file placement were done.
	DryRun bool `json:"dryRun" example:"false"`
	// Request is the request as accepted, with the runtime and app version
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
	// RemovedAt is when taking the deployment down ended, removed or
	// remove-failed. Absent until then.
	RemovedAt *time.Time `json:"removedAt,omitempty" example:"2026-11-03T12:00:00Z"`
}

// NodeDeployResult is what happened on one node.
type NodeDeployResult struct {
	// Target is the target as requested.
	Target NodeDeployTarget `json:"target"`
	// Status is pending, deploying, running, failed, dry-run or interrupted;
	// once taken down, removing, removed or remove-failed.
	Status string `json:"status" enums:"pending,deploying,running,failed,dry-run,interrupted,removing,removed,remove-failed" example:"running"`
	// Node is how the node was reached, as read from CB-Tumblebug. Absent
	// when the node could not be read. The SSH key is never included.
	Node *NodeDeployNode `json:"node,omitempty"`
	// SSHUser is the SSH user the playbooks ran as on the node: the
	// target's sshUser when given. Otherwise node.user, the one CB-Tumblebug
	// reports, is tried with the node key before the playbooks run; when the
	// node refuses it, the default users of the node image (such as ec2-user
	// on RHEL and Amazon Linux) are tried and the first it accepts is taken.
	// Taking the deployment down runs as the same user.
	SSHUser string `json:"sshUser,omitempty" example:"cb-user"`
	// SSHUserSource says where SSHUser came from: request (targets[].sshUser),
	// tumblebug (node.user, which the node accepted, or which was kept because
	// the login could not be tried or every user was refused; notes say which)
	// or fallback (an image default user, taken after the node refused
	// node.user; notes name both).
	SSHUserSource string `json:"sshUserSource,omitempty" enums:"request,tumblebug,fallback" example:"tumblebug"`
	// GPU is whether the runtime was given the node's GPUs and the NVIDIA
	// Container Toolkit was installed.
	GPU bool `json:"gpu" example:"false"`
	// GPUSource says where GPU came from: request, spec, or default when the
	// spec could not be read.
	GPUSource string `json:"gpuSource,omitempty" enums:"request,spec,default" example:"spec"`
	// GPUCheck is, for an inference service given the GPU, what compose-up
	// found on the node showing that the runtime runs on it: the method
	// (nvidia-smi or log) and the nvidia-smi compute process or the log line.
	// When the runtime is not on the GPU the node fails instead. Absent
	// without GPU, for an app and for a dry run.
	GPUCheck string `json:"gpuCheck,omitempty" example:"nvidia-smi: 3499, VLLM::EngineCore, 13524 MiB"`
	// Notes record decisions made for this node, such as why the spec could
	// not be read.
	Notes []string `json:"notes,omitempty"`
	// StageDir is the folder the files were placed in on the shared stage
	// volume: <AIAPP_STAGE_ROOT>/<deployment id>/<csp>-<node id>.
	StageDir string `json:"stageDir,omitempty" example:"/var/lib/aiapp/stage/3f9b2c4e-8a51-4d27-9e0c-5b1f6a7d2e48/aws-vm1"`
	// WorkKey names what the deployment occupies on the node: ms-<runtime>
	// for an inference service, app-<name> for an application. A later deployment of
	// the same work key to the same node replaces this one there.
	WorkKey string `json:"workKey,omitempty" example:"ms-vllm"`
	// NodeDir is the folder on the node the files were copied to,
	// /opt/aiapp/<work key>. Its compose project is aiapp-<work key>.
	NodeDir string `json:"nodeDir,omitempty" example:"/opt/aiapp/ms-vllm"`
	// Compose is the rendered docker-compose.yml of an inference service.
	Compose string `json:"compose,omitempty"`
	// ServiceURL is the inference API on the node, built from the public or
	// private IP the node was reached at, the runtime port and its API base
	// path. Inference services only.
	ServiceURL string `json:"serviceUrl,omitempty" example:"http://203.0.113.10:8000/v1"`
	// APIKey is the key the vllm service on the node takes as
	// `Authorization: Bearer <apiKey>`: the one the request gave, or the one
	// this node kept from its previous deployment of the work key, or a new
	// random one. It is kept in plain text in the deployment store, as env
	// values are. Absent for runtimes without a key.
	APIKey string `json:"apiKey,omitempty" example:"3f1c9a7e5b2d4f6a8c0e1b3d5f7a9c2e"`
	// Ports are the TCP ports the deployment serves on the node: the runtime
	// port of an inference service, the ports of an application. They are
	// opened inbound from 0.0.0.0/0 in every security group of the node once
	// the playbooks succeed. A dry run and a failed node open none; with
	// port opening turned off (AIAPP_DEPLOYMENT_OPEN_PORTS=false) none are
	// opened and notes says so.
	Ports []int `json:"ports,omitempty" example:"8000"`
	// OpenedPorts are, per security group and port, the inbound rules found
	// or added in CB-Tumblebug. Taking the deployment down closes none of
	// them, since other workloads may share the group.
	OpenedPorts []NodeOpenedPort `json:"openedPorts,omitempty"`
	// PortsError is why some ports could not be opened. The node still
	// succeeds; the ports can be opened in CB-Tumblebug by hand.
	PortsError string `json:"portsError,omitempty"`
	// Phase is what the service is doing on the node: reading-node (the node
	// and its SSH key are read from CB-Tumblebug), staging (the files are
	// placed in the stage folder), checking-ssh (the SSH user is tried),
	// waiting-node-lock (another deployment of the same work key on the node
	// runs first), install-docker, compose-up, copy-and-run or compose-down
	// (that playbook runs; steps[] holds its task), opening-ports (the served
	// ports are opened in the security groups) and done once the node
	// succeeded. A node that failed keeps the phase it failed in. Absent until
	// work on the node begins, and for records written before phases were
	// kept.
	Phase string `json:"phase,omitempty" enums:"reading-node,staging,checking-ssh,waiting-node-lock,install-docker,compose-up,copy-and-run,compose-down,opening-ports,done" example:"compose-up"`
	// PhaseStartedAt is when the node entered Phase.
	PhaseStartedAt *time.Time `json:"phaseStartedAt,omitempty" example:"2026-10-29T12:00:40Z"`
	// Steps are the playbooks run on the node, in order. A step that failed
	// ends the node.
	Steps []NodeDeployStep `json:"steps,omitempty"`
	// Error is why the node failed, or why taking it down failed.
	Error string `json:"error,omitempty"`
	// RemovedAt is when the deployment was taken down from the node.
	RemovedAt *time.Time `json:"removedAt,omitempty" example:"2026-11-03T12:00:00Z"`
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
	// User is the SSH user CB-Tumblebug reports for the node. The
	// playbooks ran as nodes[].sshUser, which differs when the target
	// gave sshUser or the node refused this user (sshUserSource).
	User string `json:"user" example:"cb-user"`
	// SpecID is the node spec in CB-Tumblebug.
	SpecID string `json:"specId,omitempty" example:"aws+ap-northeast-2+g4dn.xlarge"`
}

// Results of opening a port in a security group.
const (
	// PortAdded is a rule this deployment added.
	PortAdded = "added"
	// PortExisting is a port an inbound rule of the group already opened.
	PortExisting = "existing"
)

// NodeOpenedPort is one port open in one security group of a node.
type NodeOpenedPort struct {
	// SecurityGroupID is the CB-Tumblebug security group, in the namespace
	// of the target.
	SecurityGroupID string `json:"securityGroupId" example:"etri-shared-aws-ap-northeast-2"`
	// Port is the TCP port.
	Port int `json:"port" example:"8000"`
	// Protocol is TCP.
	Protocol string `json:"protocol" example:"TCP"`
	// Result is added when this deployment added the rule, existing when an
	// inbound rule of the group already opened the port to 0.0.0.0/0.
	Result string `json:"result" enums:"added,existing" example:"added"`
}

// NodeDeployStep is one playbook run through Semaphore.
type NodeDeployStep struct {
	// Playbook is install-docker.yml, compose-up.yml, copy-and-run.yml, or
	// compose-down.yml when the deployment was taken down.
	Playbook string `json:"playbook" example:"compose-up.yml"`
	// TaskID is the Semaphore task, kept in Semaphore with its log.
	TaskID int `json:"taskId,omitempty" example:"2147483645"`
	// SSHUser is the SSH user the playbook ran as.
	SSHUser string `json:"sshUser,omitempty" example:"cb-user"`
	// TaskURL is the page of the task in the Semaphore web UI, with its log.
	// Absent when the task never started or AIAPP_SEMAPHORE_UI_URL was not
	// set when it ran.
	TaskURL string `json:"taskUrl,omitempty" example:"http://203.0.113.5:3001/project/1/history?t=42"`
	// Status is the Semaphore task status: waiting, starting, running or
	// stopping while the task runs (read every 5 seconds), then the final one,
	// success, error, stopped or rejected. Empty while the task is being
	// registered and when it never started. TaskID and TaskURL are set as soon
	// as the task starts.
	Status string `json:"status" example:"success"`
	// CurrentTask is the Ansible task the playbook is on, from the last
	// TASK [...] line of the log so far, such as "Wait until the service
	// answers". Once the playbook ended it is the task it stopped on when it
	// did not succeed, and empty when it succeeded.
	CurrentTask string `json:"currentTask,omitempty" example:"Wait until the service answers"`
	// Output is the task log, one line per entry: the log so far while the
	// task runs, then the whole log. It is returned by
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
	Status string `json:"status" example:"running"`
	// Nodes hold, per target, the steps with their output.
	Nodes []NodeDeployNodeLogs `json:"nodes"`
}

// NodeDeployNodeLogs is the task output of one node.
type NodeDeployNodeLogs struct {
	Target NodeDeployTarget `json:"target"`
	Status string           `json:"status" example:"running"`
	Steps  []NodeDeployStep `json:"steps"`
}

// NodeDeploymentProgress is the short progress of one deployment, to poll
// until Done.
type NodeDeploymentProgress struct {
	ID   string `json:"id" example:"3f9b2c4e-8a51-4d27-9e0c-5b1f6a7d2e48"`
	Type string `json:"type" enums:"inference,app" example:"inference"`
	// Status is the deployment status, as in GET /deployments/{id}.
	Status string `json:"status" enums:"pending,deploying,running,failed,partial,dry-run,interrupted,removing,removed,remove-failed" example:"deploying"`
	// Done is false while the service still works on the deployment
	// (pending, deploying or removing) and true otherwise: polling can stop.
	Done bool `json:"done" example:"false"`
	// Running is true when the deployment ended with its service up on every
	// node (status running). A dry run, and a deployment taken down, is Done
	// without Running.
	Running bool `json:"running" example:"false"`
	// Error is why the deployment as a whole stopped, when it did.
	Error     string    `json:"error,omitempty"`
	CreatedAt time.Time `json:"createdAt" example:"2026-10-29T12:00:00Z"`
	// FinishedAt is when the last node finished deploying; RemovedAt when
	// taking it down ended.
	FinishedAt *time.Time `json:"finishedAt,omitempty" example:"2026-10-29T12:05:00Z"`
	RemovedAt  *time.Time `json:"removedAt,omitempty"`
	// ElapsedSeconds runs from createdAt to finishedAt (or removedAt once
	// taken down), or to now while the deployment is not done.
	ElapsedSeconds int64 `json:"elapsedSeconds" example:"312"`
	// Count is how many nodes are in each state.
	Count NodeProgressCount `json:"count"`
	// Nodes are the per node progress, in request order.
	Nodes []NodeProgress `json:"nodes"`
}

// NodeProgressCount counts the nodes of a deployment by state.
type NodeProgressCount struct {
	Total int `json:"total" example:"2"`
	// InProgress are pending, deploying or removing.
	InProgress int `json:"inProgress" example:"1"`
	// Running are running.
	Running int `json:"running" example:"1"`
	// Failed are failed, interrupted or remove-failed.
	Failed int `json:"failed" example:"0"`
	// DryRun are dry-run.
	DryRun int `json:"dryRun" example:"0"`
	// Removed are removed.
	Removed int `json:"removed" example:"0"`
}

// NodeProgress is the short progress of one node.
type NodeProgress struct {
	Target NodeDeployTarget `json:"target"`
	// Status is the node status, as in GET /deployments/{id}.
	Status string `json:"status" example:"deploying"`
	// Done is true once the node is no longer pending, deploying or removing.
	Done bool `json:"done" example:"false"`
	// Phase is nodes[].phase of the record: where the node is, or where it
	// failed.
	Phase          string     `json:"phase,omitempty" example:"compose-up"`
	PhaseStartedAt *time.Time `json:"phaseStartedAt,omitempty"`
	// Step is the last playbook started on the node, without its log.
	Step *NodeDeployStep `json:"step,omitempty"`
	// StepsDone counts the playbooks of StepsTotal that succeeded. StepsTotal
	// is 2 for a deployment (install-docker, then compose-up or
	// copy-and-run), 1 while or after taking it down (compose-down), and 0
	// for a dry run.
	StepsDone  int `json:"stepsDone" example:"1"`
	StepsTotal int `json:"stepsTotal" example:"2"`
	// Error is why the node failed.
	Error string `json:"error,omitempty"`
	// Host is the address the node was reached at (nodes[].node.host).
	Host string `json:"host,omitempty" example:"203.0.113.10"`
	// ServiceURL is the inference API on the node (inference only).
	ServiceURL string `json:"serviceUrl,omitempty" example:"http://203.0.113.10:8000/v1"`
	// Ports are the TCP ports the deployment serves on the node.
	Ports []int `json:"ports,omitempty" example:"8000"`
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
