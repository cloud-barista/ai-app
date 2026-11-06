package nodedeploy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"sync"

	"github.com/rs/zerolog/log"

	"github.com/ish-hcc/ai-agent-proto/internal/model"
	"github.com/ish-hcc/ai-agent-proto/internal/modelcat"
	"github.com/ish-hcc/ai-agent-proto/internal/tumblebug"
)

// endpointReadParallel bounds the nodes read from CB-Tumblebug at once while
// listing endpoints.
const endpointReadParallel = 8

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
	// Available, when set, keeps only the endpoints whose available is the
	// same.
	Available *bool
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
// The work key has an endpoint only when that deployment is running on the
// node. A later deployment that failed or was interrupted after it started
// compose-up or copy-and-run leaves none: the same compose project was
// recreated, so the containers of the earlier deployment are gone or in an
// unknown state. A later deployment that failed before starting its service
// does not count, and the earlier one stays. One still pending or deploying
// leaves none until it ends. Dry-run deployments never count.
//
// Every node with an endpoint is then read from CB-Tumblebug once (see
// readNodes). The URLs are built from its current address and only when it is
// Running, since the IP of a node may change when it is suspended and resumed.
func (s *Service) Endpoints(ctx context.Context, filter EndpointFilter) []model.Endpoint {
	type found struct {
		d       *model.NodeDeployment
		n       *model.NodeDeployResult
		workKey string
	}
	decided := make(map[string]bool)
	var serving []found
	targets := make(map[string]model.NodeDeployTarget)
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
			if n.Status == model.NodeDeployRunning && n.Node != nil {
				serving = append(serving, found{d: &d, n: n, workKey: place.WorkKey})
				targets[n.Target.Key()] = n.Target
			}
		}
	}

	nodes := s.readNodes(ctx, targets)
	out := make([]model.Endpoint, 0, len(serving))
	for _, f := range serving {
		e := endpointOf(f.d, f.n, f.workKey, nodes[f.n.Target.Key()])
		if filter.Available != nil && *filter.Available != e.Available {
			continue
		}
		out = append(out, e)
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

// nodeNow is a node as CB-Tumblebug gives it when the endpoints are listed.
type nodeNow struct {
	// status is the CB-Tumblebug node status, or model.EndpointNodeUnknown or
	// model.EndpointNodeNotFound.
	status string
	// host is the public IP, or the private IP when the node has none.
	host string
}

// readNodes reads every target from CB-Tumblebug, at most
// endpointReadParallel at once, keyed by target key. A node that cannot be
// read, or that CB-Tumblebug gives no status for, is unknown, and one
// CB-Tumblebug no longer has is not-found. The upstream error is logged,
// never returned: it may hold the CB-Tumblebug address.
func (s *Service) readNodes(ctx context.Context, targets map[string]model.NodeDeployTarget) map[string]nodeNow {
	out := make(map[string]nodeNow, len(targets))
	var mu sync.Mutex
	var wg sync.WaitGroup
	slots := make(chan struct{}, endpointReadParallel)
	for key, t := range targets {
		wg.Add(1)
		go func() {
			defer wg.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			now := nodeNow{status: model.EndpointNodeUnknown}
			node, err := s.tb.GetNode(ctx, t.NsID, t.InfraID, t.NodeID)
			switch {
			case err == nil:
				now.host = firstNonEmpty(node.PublicIP, node.PrivateIP)
				if node.Status != "" {
					now.status = node.Status
				}
			case errors.Is(err, tumblebug.ErrNodeNotFound):
				now.status = model.EndpointNodeNotFound
			default:
				log.Warn().Str("nsId", t.NsID).Str("infraId", t.InfraID).Str("nodeId", t.NodeID).
					Str("error", err.Error()).Msg("Failed to read the node of an endpoint")
			}
			mu.Lock()
			out[key] = now
			mu.Unlock()
		}()
	}
	wg.Wait()
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

// endpointOf builds the endpoint of node result n of d, which is running
// there, on node as CB-Tumblebug gives it now.
func endpointOf(d *model.NodeDeployment, n *model.NodeDeployResult, workKey string, node nodeNow) model.Endpoint {
	e := model.Endpoint{
		NsID: n.Target.NsID, InfraID: n.Target.InfraID, NodeID: n.Target.NodeID,
		CSP: n.Node.CSP, Region: n.Node.Region, Zone: n.Node.Zone,
		NodeStatus: node.status, Available: node.status == model.EndpointNodeRunning && node.host != "",
		Type: d.Type, WorkKey: workKey, DeploymentID: d.ID, DeployedAt: d.CreatedAt,
		Ports: n.Ports, OpenedPorts: n.OpenedPorts, PortsError: n.PortsError, Notes: n.Notes,
		PortsOpened: portsOpened(n),
	}
	if n.FinishedAt != nil {
		e.DeployedAt = *n.FinishedAt
	}
	if e.Available {
		e.Host = node.host
	}
	switch d.Type {
	case model.NodeDeployTypeInference:
		inf := d.Request.Inference
		e.RuntimeID, e.ModelID, e.ArtifactID = inf.RuntimeID, inf.ModelID, inf.ArtifactID
		e.APIKey = n.APIKey
		if !e.Available {
			break
		}
		e.URL = withHost(n.ServiceURL, e.Host)
		if runtime, err := modelcat.Runtime(inf.RuntimeID); err == nil && runtime.HealthPath != "" {
			e.HealthURL = fmt.Sprintf("http://%s%s", net.JoinHostPort(e.Host, strconv.Itoa(runtime.Port)), runtime.HealthPath)
		}
	case model.NodeDeployTypeApp:
		e.Name, e.Version = d.Request.App.Name, d.Request.App.Version
		if !e.Available {
			break
		}
		for _, p := range n.Ports {
			e.URLs = append(e.URLs, model.EndpointURL{Port: p, URL: "http://" + net.JoinHostPort(e.Host, strconv.Itoa(p))})
		}
	}
	return e
}

// withHost is the service URL recorded at deploy time with its host replaced
// by host, keeping its port and path, or "" when none was recorded.
func withHost(recorded, host string) string {
	u, err := url.Parse(recorded)
	if err != nil || u.Host == "" {
		return ""
	}
	if port := u.Port(); port != "" {
		u.Host = net.JoinHostPort(host, port)
	} else if ip := net.ParseIP(host); ip != nil && ip.To4() == nil {
		u.Host = "[" + host + "]"
	} else {
		u.Host = host
	}
	return u.String()
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
