package nodedeploy

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/ish-hcc/ai-agent-proto/internal/model"
	"github.com/ish-hcc/ai-agent-proto/internal/tumblebug"
)

// openPortsCIDR is where the ports a deployment opens are reachable from.
const openPortsCIDR = "0.0.0.0/0"

// servedPorts are the TCP ports a deployment serves on a node: the runtime
// port of an inference service; the ports of the app request, or else of the
// application version.
func servedPorts(req *model.NodeDeploymentReq, p *plan) []int {
	if req.Type == model.NodeDeployTypeInference {
		return []int{p.runtime.Port}
	}
	if len(req.App.Ports) > 0 {
		return slices.Clone(req.App.Ports)
	}
	return slices.Clone(p.appPorts)
}

// openPorts opens ports inbound over TCP from openPortsCIDR in every security
// group, and reports per group and port what it found or added, and why a
// group could not be changed.
//
// A group is read first, and only the ports no inbound rule opens yet are
// sent: CB-Tumblebug refuses a request holding a rule the group already has.
// A group that cannot be read gets every port sent.
func (s *Service) openPorts(ctx context.Context, nsID string, sgIDs []string, ports []int) ([]model.NodeOpenedPort, []string) {
	var opened []model.NodeOpenedPort
	var failures []string
	for _, sgID := range sgIDs {
		missing := ports
		if sg, err := s.tb.GetSecurityGroup(ctx, nsID, sgID); err == nil {
			missing = nil
			for _, port := range ports {
				if slices.ContainsFunc(sg.FirewallRules, func(r tumblebug.FirewallRuleInfo) bool { return opensPort(r, port) }) {
					opened = append(opened, openedPort(sgID, port, model.PortExisting))
				} else {
					missing = append(missing, port)
				}
			}
		}
		if len(missing) == 0 {
			continue
		}
		rule := tumblebug.FirewallRuleReq{
			Direction: tumblebug.DirectionInbound, Protocol: tumblebug.ProtocolTCP,
			Ports: joinPorts(missing), CIDR: openPortsCIDR,
		}
		resp, err := s.tb.AddFirewallRules(ctx, nsID, sgID, []tumblebug.FirewallRuleReq{rule})
		switch {
		case err != nil:
			failures = append(failures, fmt.Sprintf("ports %s: %s", rule.Ports, err))
			continue
		case !resp.Success:
			failures = append(failures, fmt.Sprintf("ports %s in security group %q: %s", rule.Ports, sgID, resp.Message))
			continue
		}
		for _, port := range missing {
			opened = append(opened, openedPort(sgID, port, model.PortAdded))
		}
	}
	return opened, failures
}

// opensPort reports whether rule lets port in over TCP from openPortsCIDR.
func opensPort(rule tumblebug.FirewallRuleInfo, port int) bool {
	if !strings.EqualFold(rule.Direction, tumblebug.DirectionInbound) || rule.CIDR != openPortsCIDR {
		return false
	}
	if !strings.EqualFold(rule.Protocol, tumblebug.ProtocolTCP) && !strings.EqualFold(rule.Protocol, "ALL") {
		return false
	}
	from, to, isRange := strings.Cut(strings.TrimSpace(rule.Port), "-")
	low, err := strconv.Atoi(from)
	if err != nil {
		return false
	}
	high := low
	if isRange {
		if high, err = strconv.Atoi(to); err != nil {
			return false
		}
	}
	return low <= port && port <= high
}

func openedPort(sgID string, port int, result string) model.NodeOpenedPort {
	return model.NodeOpenedPort{SecurityGroupID: sgID, Port: port, Protocol: tumblebug.ProtocolTCP, Result: result}
}

func joinPorts(ports []int) string {
	parts := make([]string, len(ports))
	for i, port := range ports {
		parts[i] = strconv.Itoa(port)
	}
	return strings.Join(parts, ",")
}

// uniqueIDs drops empty and repeated identifiers, keeping the first order.
func uniqueIDs(ids []string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}
