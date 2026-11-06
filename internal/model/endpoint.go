package model

import "time"

// Node statuses of an endpoint besides those CB-Tumblebug gives.
const (
	// EndpointNodeRunning is the CB-Tumblebug node status under which an
	// endpoint is available.
	EndpointNodeRunning = "Running"
	// EndpointNodeUnknown is a node CB-Tumblebug could not be read for.
	EndpointNodeUnknown = "unknown"
	// EndpointNodeNotFound is a node CB-Tumblebug no longer has.
	EndpointNodeNotFound = "not-found"
)

// Endpoint is one inference service or application serving on a node now, as
// the deployment records tell it: the latest deployment of its work key on
// the node succeeded there and has not been taken down. Whether it can be
// reached, and at which address, follows the node as CB-Tumblebug gives it
// when the endpoints are listed.
type Endpoint struct {
	// NsID is the CB-Tumblebug namespace of the node.
	NsID string `json:"nsId" example:"etri"`
	// InfraID is the CB-Tumblebug Infra of the node.
	InfraID string `json:"infraId" example:"travel"`
	// NodeID is the node within the Infra.
	NodeID string `json:"nodeId" example:"vm1"`
	// CSP is the cloud of the node, in lower case.
	CSP string `json:"csp,omitempty" example:"aws"`
	// Region and Zone are where the node runs.
	Region string `json:"region,omitempty" example:"ap-northeast-2"`
	Zone   string `json:"zone,omitempty" example:"ap-northeast-2a"`
	// NodeStatus is the node status CB-Tumblebug gives when the endpoints are
	// listed, such as Running, Suspended, Terminated or Failed; unknown when
	// CB-Tumblebug could not be read, not-found when it no longer has the
	// node.
	NodeStatus string `json:"nodeStatus" example:"Running"`
	// Available is true when the node is Running and has an address; only
	// then are host and the URLs set.
	Available bool `json:"available" example:"true"`
	// Host is the address the URLs are built from: the current public IP of
	// the node, or its private IP when it has none. Absent when not
	// available.
	Host string `json:"host,omitempty" example:"203.0.113.10"`
	// Type is inference or app.
	Type string `json:"type" enums:"inference,app" example:"inference"`
	// WorkKey is what serves on the node: ms-<runtimeId> for an inference
	// service, app-<name> for an application. A node serves one endpoint per
	// work key.
	WorkKey string `json:"workKey" example:"ms-vllm"`
	// DeploymentID is the deployment serving the work key on the node; read
	// it with GET /deployments/{id} and take it down with
	// DELETE /deployments/{id}.
	DeploymentID string `json:"deploymentId" example:"3f9b2c4e-8a51-4d27-9e0c-5b1f6a7d2e48"`
	// DeployedAt is when the deployment finished on the node.
	DeployedAt time.Time `json:"deployedAt" example:"2026-11-03T12:05:00Z"`

	// RuntimeID is the inference runtime profile. Inference only.
	RuntimeID string `json:"runtimeId,omitempty" example:"vllm"`
	// ModelID is the model of the model catalog served. Inference only.
	ModelID string `json:"modelId,omitempty" example:"qwen3-embedding-0.6b"`
	// ArtifactID is the artifact of that model. Inference only.
	ArtifactID string `json:"artifactId,omitempty" example:"hf-bf16"`
	// URL is the OpenAI compatible API of an inference service:
	// http://<host>:<runtime port><runtime apiBasePath>. Inference only, and
	// only when available.
	URL string `json:"url,omitempty" example:"http://203.0.113.10:8000/v1"`
	// HealthURL answers 200 once the runtime is up:
	// http://<host>:<runtime port><runtime healthPath>. Inference only, and
	// only when available and the runtime profile names a health path.
	HealthURL string `json:"healthUrl,omitempty" example:"http://203.0.113.10:8000/health"`

	// Name is the uploaded application. App only.
	Name string `json:"name,omitempty" example:"open-webui"`
	// Version is the application version deployed. App only.
	Version int `json:"version,omitempty" example:"1"`
	// URLs are http://<host>:<port> for every port the application serves.
	// App only; absent when not available or the application serves no port.
	URLs []EndpointURL `json:"urls,omitempty"`

	// Ports are the TCP ports the deployment serves on the node.
	Ports []int `json:"ports,omitempty" example:"8000"`
	// PortsOpened is true when every port in ports was found or added as an
	// inbound rule in a CB-Tumblebug security group of the node and no port
	// failed. False means the URLs may not be reachable from outside the
	// node: portsError or notes say why.
	PortsOpened bool `json:"portsOpened" example:"true"`
	// OpenedPorts are, per security group and port, the inbound rules found
	// or added when the deployment ran.
	OpenedPorts []NodeOpenedPort `json:"openedPorts,omitempty"`
	// PortsError is why some ports could not be opened.
	PortsError string `json:"portsError,omitempty"`
	// Notes are the notes of the node result, such as port opening being
	// turned off.
	Notes []string `json:"notes,omitempty"`
}

// EndpointURL is the address of one port an application serves.
type EndpointURL struct {
	// Port is the TCP port.
	Port int `json:"port" example:"3000"`
	// URL is http://<host>:<port>.
	URL string `json:"url" example:"http://203.0.113.10:3000"`
}

// EndpointListResp is the endpoint listing body.
type EndpointListResp struct {
	// Endpoints are ordered by nsId, infraId, nodeId and workKey.
	Endpoints []Endpoint `json:"endpoints"`
}
