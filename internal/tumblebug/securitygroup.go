package tumblebug

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
)

// Firewall rule directions and protocols accepted by CB-Tumblebug.
const (
	DirectionInbound = "inbound"
	ProtocolTCP      = "TCP"
)

// FirewallRuleReq is one rule to add to a security group.
//
// The field names are capitalized on the wire, matching the upstream struct.
type FirewallRuleReq struct {
	Direction string `json:"Direction"`
	Protocol  string `json:"Protocol"`
	// Ports accepts a single port, a range, or a comma separated list.
	Ports string `json:"Ports"`
	CIDR  string `json:"CIDR"`
}

// SecurityGroupUpdateReq adds rules to an existing security group.
type SecurityGroupUpdateReq struct {
	FirewallRules []FirewallRuleReq `json:"firewallRules"`
}

// FirewallRuleInfo is one rule as CB-Tumblebug reports it.
//
// The response names the single port field "Port", not "Ports" as the request
// does, so it needs its own type.
type FirewallRuleInfo struct {
	Direction string `json:"Direction"`
	Protocol  string `json:"Protocol"`
	Port      string `json:"Port"`
	CIDR      string `json:"CIDR"`
}

// SecurityGroupInfo is a security group as CB-Tumblebug knows it.
type SecurityGroupInfo struct {
	ID            string             `json:"id"`
	Name          string             `json:"name"`
	VNetID        string             `json:"vNetId"`
	FirewallRules []FirewallRuleInfo `json:"firewallRules"`
}

// SecurityGroupUpdateResp reports the outcome of a rule change.
type SecurityGroupUpdateResp struct {
	Success bool               `json:"success"`
	ID      string             `json:"id"`
	Name    string             `json:"name"`
	Message string             `json:"message"`
	Updated *SecurityGroupInfo `json:"updated,omitempty"`
}

// AddFirewallRules adds inbound or outbound rules to an existing security group.
func (c *Client) AddFirewallRules(ctx context.Context, nsID, sgID string, rules []FirewallRuleReq) (*SecurityGroupUpdateResp, error) {
	path := fmt.Sprintf("/ns/%s/resources/securityGroup/%s/rules",
		url.PathEscape(nsID), url.PathEscape(sgID))

	body := &SecurityGroupUpdateReq{FirewallRules: rules}
	result := &SecurityGroupUpdateResp{}
	if err := c.Do(ctx, http.MethodPost, path, nil, body, result); err != nil {
		return nil, fmt.Errorf("failed to add firewall rules to security group %q: %w", sgID, err)
	}
	return result, nil
}

// GetSecurityGroup reads one security group.
func (c *Client) GetSecurityGroup(ctx context.Context, nsID, sgID string) (*SecurityGroupInfo, error) {
	path := fmt.Sprintf("/ns/%s/resources/securityGroup/%s",
		url.PathEscape(nsID), url.PathEscape(sgID))

	result := &SecurityGroupInfo{}
	if err := c.Do(ctx, http.MethodGet, path, nil, nil, result); err != nil {
		return nil, fmt.Errorf("failed to get security group %q: %w", sgID, err)
	}
	return result, nil
}
