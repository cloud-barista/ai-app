package tumblebug

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// defaultSSHPort is used when a node reports no SSH port.
const defaultSSHPort = 22

// Node is one node of an Infra, with the fields a deployment needs to reach it.
//
// The fields follow model.NodeInfo of the CB-Tumblebug 0.13 OpenAPI document,
// returned by GET /ns/{nsId}/infra/{infraId}/node/{nodeId}.
type Node struct {
	ID               string         `json:"id"`
	Name             string         `json:"name"`
	Status           string         `json:"status"`
	PublicIP         string         `json:"publicIP"`
	PrivateIP        string         `json:"privateIP"`
	SSHPort          int            `json:"sshPort"`
	NodeUserName     string         `json:"nodeUserName"`
	SSHKeyID         string         `json:"sshKeyId"`
	ConnectionName   string         `json:"connectionName"`
	ConnectionConfig NodeConnConfig `json:"connectionConfig"`
	// Region is the region and zone as the node itself records them. It is
	// the fallback when the connection config carries no assigned region.
	Region NodeRegion `json:"region"`
}

// NodeConnConfig is the part of model.ConnConfig that says where a node runs.
type NodeConnConfig struct {
	ProviderName   string         `json:"providerName"`
	RegionZoneInfo RegionZoneInfo `json:"regionZoneInfo"`
}

// RegionZoneInfo is model.RegionZoneInfo.
type RegionZoneInfo struct {
	AssignedRegion string `json:"assignedRegion"`
	AssignedZone   string `json:"assignedZone"`
}

// NodeRegion is model.RegionInfo.
type NodeRegion struct {
	Region string `json:"region"`
	Zone   string `json:"zone"`
}

// SSHKey is an SSH key resource with the fields a deployment needs to log in.
//
// PrivateKey is never encoded to JSON and never printed by the fmt verbs, so a
// key that ends up in a response, a log or an error stays out of it.
type SSHKey struct {
	ID               string `json:"id"`
	Username         string `json:"username"`
	VerifiedUsername string `json:"verifiedUsername"`
	PrivateKey       string `json:"-"`
}

// String keeps the private key out of %v and %+v.
func (k SSHKey) String() string {
	return fmt.Sprintf("SSHKey{ID:%s Username:%s VerifiedUsername:%s PrivateKey:%s}",
		k.ID, k.Username, k.VerifiedUsername, maskedIfSet(k.PrivateKey))
}

// GoString keeps the private key out of %#v.
func (k SSHKey) GoString() string { return k.String() }

// sshKeyWire decodes model.SshKeyInfo, whose privateKey SSHKey does not decode.
type sshKeyWire struct {
	ID               string `json:"id"`
	Username         string `json:"username"`
	VerifiedUsername string `json:"verifiedUsername"`
	PrivateKey       string `json:"privateKey"`
}

// NodeAccess is how to reach a node over SSH, and where it runs.
type NodeAccess struct {
	Host string `json:"host"`
	Port int    `json:"port"`
	User string `json:"user"`
	// PrivateKey is never encoded to JSON and never printed by the fmt verbs.
	PrivateKey string `json:"-"`
	CSP        string `json:"csp"`
	Region     string `json:"region"`
	Zone       string `json:"zone"`
}

// String keeps the private key out of %v and %+v.
func (a NodeAccess) String() string {
	return fmt.Sprintf("NodeAccess{Host:%s Port:%d User:%s PrivateKey:%s CSP:%s Region:%s Zone:%s}",
		a.Host, a.Port, a.User, maskedIfSet(a.PrivateKey), a.CSP, a.Region, a.Zone)
}

// GoString keeps the private key out of %#v.
func (a NodeAccess) GoString() string { return a.String() }

func maskedIfSet(secret string) string {
	if secret == "" {
		return ""
	}
	return "<redacted>"
}

// GetNode reads one node of an Infra.
func (c *Client) GetNode(ctx context.Context, nsID, infraID, nodeID string) (*Node, error) {
	path := fmt.Sprintf("%s/node/%s", infraPath(nsID, infraID), url.PathEscape(nodeID))

	result := &Node{}
	if err := c.Do(ctx, http.MethodGet, path, nil, nil, result); err != nil {
		return nil, fmt.Errorf("failed to get node %q of infra %q: %w", nodeID, infraID, err)
	}
	return result, nil
}

// GetSSHKey reads an SSH key resource, private key included.
func (c *Client) GetSSHKey(ctx context.Context, nsID, sshKeyID string) (*SSHKey, error) {
	path := fmt.Sprintf("/ns/%s/resources/sshKey/%s", url.PathEscape(nsID), url.PathEscape(sshKeyID))

	wire := &sshKeyWire{}
	if err := c.Do(ctx, http.MethodGet, path, nil, nil, wire); err != nil {
		return nil, fmt.Errorf("failed to get ssh key %q: %w", sshKeyID, err)
	}
	return &SSHKey{
		ID:               wire.ID,
		Username:         wire.Username,
		VerifiedUsername: wire.VerifiedUsername,
		PrivateKey:       wire.PrivateKey,
	}, nil
}

// GetNodeAccess reads a node and its SSH key, and resolves how to reach it.
func (c *Client) GetNodeAccess(ctx context.Context, nsID, infraID, nodeID string) (*NodeAccess, error) {
	node, err := c.GetNode(ctx, nsID, infraID, nodeID)
	if err != nil {
		return nil, err
	}
	if node.SSHKeyID == "" {
		return nil, fmt.Errorf("node %q of infra %q has no ssh key", nodeID, infraID)
	}
	key, err := c.GetSSHKey(ctx, nsID, node.SSHKeyID)
	if err != nil {
		return nil, err
	}
	return ResolveNodeAccess(node, key)
}

// ResolveNodeAccess derives how to reach node with key.
//
// The public IP is preferred and the private IP is the fallback, for nodes
// reached from inside their own network. The user is the node's own user name,
// then the user name CB-Tumblebug verified on the key, then the key's user name.
func ResolveNodeAccess(node *Node, key *SSHKey) (*NodeAccess, error) {
	if node == nil {
		return nil, errors.New("no node")
	}
	if key == nil {
		return nil, fmt.Errorf("node %q: no ssh key", node.ID)
	}

	host := firstNonEmpty(node.PublicIP, node.PrivateIP)
	if host == "" {
		return nil, fmt.Errorf("node %q has neither a public nor a private IP", node.ID)
	}
	user := firstNonEmpty(node.NodeUserName, key.VerifiedUsername, key.Username)
	if user == "" {
		return nil, fmt.Errorf("node %q: no user name on the node or on ssh key %q", node.ID, key.ID)
	}
	if strings.TrimSpace(key.PrivateKey) == "" {
		return nil, fmt.Errorf("node %q: ssh key %q has no private key", node.ID, key.ID)
	}
	port := node.SSHPort
	if port <= 0 {
		port = defaultSSHPort
	}

	zoneInfo := node.ConnectionConfig.RegionZoneInfo
	return &NodeAccess{
		Host:       host,
		Port:       port,
		User:       user,
		PrivateKey: key.PrivateKey,
		CSP:        strings.ToLower(node.ConnectionConfig.ProviderName),
		Region:     firstNonEmpty(zoneInfo.AssignedRegion, node.Region.Region),
		Zone:       firstNonEmpty(zoneInfo.AssignedZone, node.Region.Zone),
	}, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}
