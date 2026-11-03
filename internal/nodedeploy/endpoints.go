package nodedeploy

import (
	"context"
	"fmt"
	"slices"
	"sort"

	"github.com/ish-hcc/ai-agent-proto/internal/model"
	"github.com/ish-hcc/ai-agent-proto/internal/modelcat"
)

// EndpointFilter narrows an endpoint listing. Every set field must match
// exactly; empty fields match anything.
type EndpointFilter struct {
	// NsID is the CB-Tumblebug namespace of the node.
	NsID string
	// Type is inference or app.
	Type string
	// InfraID is the CB-Tumblebug Infra of the node.
	InfraID string
	// NodeID is the node within the Infra.
	NodeID string
}

func (f EndpointFilter) matches(typ string, t model.NodeDeployTarget) bool {
	return (f.Type == "" || f.Type == typ) && (f.NsID == "" || f.NsID == t.NsID) &&
		(f.InfraID == "" || f.InfraID == t.InfraID) && (f.NodeID == "" || f.NodeID == t.NodeID)
}

// Endpoints lists what serves on the nodes now, derived from the deployment
// records, ordered by namespace, Infra, node and work key.
//
// On every node, each work key is decided by the latest deployment that
// touched it there, the order laterOn takes: one that holds the work key (see
// holdsWorkKey), or one that started its service and was taken down since,
// since compose-down of the work key's project stopped whatever ran in it.
// The work key has an endpoint only when that deployment succeeded on the
// node. A later deployment that failed or was interrupted after it started
// compose-up or copy-and-run leaves none: the same compose project was
// recreated, so the containers of the earlier deployment are gone or in an
// unknown state. A later deployment that failed before starting its service
// does not count, and the earlier one stays. One still pending or running
// leaves none until it ends. Dry-run deployments never count.
func (s *Service) Endpoints(ctx context.Context, filter EndpointFilter) []model.Endpoint {
	decided := make(map[string]bool)
	out := make([]model.Endpoint, 0)
	for _, d := range s.store.List() {
		if d.DryRun {
			continue
		}
		place, err := placementFor(&d.Request)
		if err != nil {
			continue
		}
		for i := range d.Nodes {
			n := &d.Nodes[i]
			if !filter.matches(d.Type, n.Target) {
				continue
			}
			key := n.Target.Key() + " " + place.WorkKey
			if decided[key] || !touchedWorkKey(n) {
				continue
			}
			decided[key] = true
			if n.Status == model.NodeDeploySucceeded && n.Node != nil {
				out = append(out, endpointOf(&d, n, place.WorkKey))
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.NsID != b.NsID {
			return a.NsID < b.NsID
		}
		if a.InfraID != b.InfraID {
			return a.InfraID < b.InfraID
		}
		if a.NodeID != b.NodeID {
			return a.NodeID < b.NodeID
		}
		return a.WorkKey < b.WorkKey
	})
	return out
}

// touchedWorkKey reports whether node result n decides its work key on its
// node: it holds the work key, or it started its service there and was taken
// down since.
func touchedWorkKey(n *model.NodeDeployResult) bool {
	if holdsWorkKey(n) {
		return true
	}
	return (n.Status == model.NodeDeployRemoving || n.Status == model.NodeDeployRemoved) && startedService(n)
}

// endpointOf builds the endpoint of node result n of d, which succeeded.
func endpointOf(d *model.NodeDeployment, n *model.NodeDeployResult, workKey string) model.Endpoint {
	e := model.Endpoint{
		NsID: n.Target.NsID, InfraID: n.Target.InfraID, NodeID: n.Target.NodeID,
		CSP: n.Node.CSP, Region: n.Node.Region, Zone: n.Node.Zone, Host: n.Node.Host,
		Type: d.Type, WorkKey: workKey, DeploymentID: d.ID, DeployedAt: d.CreatedAt,
		Ports: n.Ports, OpenedPorts: n.OpenedPorts, PortsError: n.PortsError, Notes: n.Notes,
		PortsOpened: portsOpened(n),
	}
	if n.FinishedAt != nil {
		e.DeployedAt = *n.FinishedAt
	}
	switch d.Type {
	case model.NodeDeployTypeInference:
		inf := d.Request.Inference
		e.RuntimeID, e.ModelID, e.ArtifactID = inf.RuntimeID, inf.ModelID, inf.ArtifactID
		e.URL = n.ServiceURL
		if runtime, err := modelcat.Runtime(inf.RuntimeID); err == nil && runtime.HealthPath != "" {
			e.HealthURL = fmt.Sprintf("http://%s:%d%s", n.Node.Host, runtime.Port, runtime.HealthPath)
		}
	case model.NodeDeployTypeApp:
		e.Name, e.Version = d.Request.App.Name, d.Request.App.Version
		for _, p := range n.Ports {
			e.URLs = append(e.URLs, model.EndpointURL{Port: p, URL: fmt.Sprintf("http://%s:%d", n.Node.Host, p)})
		}
	}
	return e
}

// portsOpened reports whether every port n serves was opened in some security
// group of the node, with no port failing.
func portsOpened(n *model.NodeDeployResult) bool {
	if len(n.Ports) == 0 || n.PortsError != "" {
		return false
	}
	for _, p := range n.Ports {
		if !slices.ContainsFunc(n.OpenedPorts, func(o model.NodeOpenedPort) bool { return o.Port == p }) {
			return false
		}
	}
	return true
}
