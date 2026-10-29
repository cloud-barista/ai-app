package tumblebug

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ish-hcc/ai-agent-proto/internal/config"
)

// testPrivateKey is fake. Its PEM label is split so that secret scanners do
// not take it for a real key.
const testPrivateKey = "-----BEGIN OPENSSH " + "PRIVATE KEY-----\nc2VjcmV0LWtleS1ib2R5\n-----END OPENSSH " + "PRIVATE KEY-----\n"

// nodeBody is a trimmed model.NodeInfo as CB-Tumblebug 0.13 answers it.
const nodeBody = `{
	"id": "vm1", "name": "vm1", "status": "Running",
	"publicIP": "203.0.113.10", "privateIP": "10.0.0.5", "sshPort": 22,
	"nodeUserName": "cb-user", "sshKeyId": "aws-key", "specId": "aws+ap-northeast-2+t3.small",
	"connectionName": "aws-ap-northeast-2",
	"connectionConfig": {
		"configName": "aws-ap-northeast-2", "providerName": "AWS",
		"regionZoneInfo": {"assignedRegion": "ap-northeast-2", "assignedZone": "ap-northeast-2a"}
	},
	"region": {"region": "ap-northeast-2", "zone": "ap-northeast-2a"},
	"label": {"sys.id": "vm1"}
}`

func newNodeServer(t *testing.T) *httptest.Server {
	t.Helper()
	keyBody, _ := json.Marshal(map[string]any{
		"id": "aws-key", "username": "key-user", "verifiedUsername": "cb-user",
		"privateKey": testPrivateKey, "publicKey": "ssh-ed25519 AAAA",
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if user, pass, ok := r.BasicAuth(); !ok || user != "tb" || pass != "pw" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /tumblebug/ns/etri/infra/travel/node/vm1":
			_, _ = io.WriteString(w, nodeBody)
		case "GET /tumblebug/ns/etri/resources/sshKey/aws-key":
			_, _ = w.Write(keyBody)
		case "GET /tumblebug/ns/etri/control/infra/travel/node/vm1":
			_, _ = io.WriteString(w, `{"message":"`+r.URL.Query().Get("action")+` vm1 done"}`)
		default:
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"message":"not found"}`)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newTestClient(srv *httptest.Server) *Client {
	return NewClient(config.TumblebugConfig{
		BaseURL: srv.URL + "/tumblebug", Username: "tb", Password: "pw", Timeout: 5 * time.Second,
	})
}

func TestGetNodeAccess(t *testing.T) {
	c := newTestClient(newNodeServer(t))

	got, err := c.GetNodeAccess(context.Background(), "etri", "travel", "vm1")
	if err != nil {
		t.Fatalf("GetNodeAccess: %v", err)
	}
	want := NodeAccess{
		Host: "203.0.113.10", Port: 22, User: "cb-user", PrivateKey: testPrivateKey,
		CSP: "aws", Region: "ap-northeast-2", Zone: "ap-northeast-2a",
	}
	if *got != want {
		t.Errorf("access = %+v, want %+v", *got, want)
	}
}

func TestGetNodeError(t *testing.T) {
	c := newTestClient(newNodeServer(t))

	_, err := c.GetNode(context.Background(), "etri", "travel", "missing")
	if err == nil || StatusCode(err) != http.StatusInternalServerError || !strings.Contains(err.Error(), `"missing"`) {
		t.Errorf("GetNode error = %v, want a 500 naming the node", err)
	}
}

func TestSSHKeyNeverPrintsThePrivateKey(t *testing.T) {
	c := newTestClient(newNodeServer(t))

	key, err := c.GetSSHKey(context.Background(), "etri", "aws-key")
	if err != nil {
		t.Fatalf("GetSSHKey: %v", err)
	}
	if key.PrivateKey != testPrivateKey {
		t.Fatalf("private key was not decoded")
	}
	access, err := c.GetNodeAccess(context.Background(), "etri", "travel", "vm1")
	if err != nil {
		t.Fatalf("GetNodeAccess: %v", err)
	}
	encodedKey, _ := json.Marshal(key)
	encodedAccess, _ := json.Marshal(access)
	for _, text := range []string{
		fmt.Sprintf("%v", key), fmt.Sprintf("%+v", key), fmt.Sprintf("%#v", key), fmt.Sprintf("%v", *key),
		fmt.Sprintf("%v", access), fmt.Sprintf("%+v", *access), fmt.Sprintf("%#v", access),
		string(encodedKey), string(encodedAccess),
	} {
		if strings.Contains(text, "c2VjcmV0LWtleS1ib2R5") {
			t.Errorf("private key printed: %s", text)
		}
	}
}

func TestResolveNodeAccess(t *testing.T) {
	key := &SSHKey{ID: "k", Username: "key-user", VerifiedUsername: "verified", PrivateKey: testPrivateKey}

	cases := []struct {
		name    string
		node    Node
		key     *SSHKey
		want    NodeAccess
		wantErr string
	}{
		{
			name: "private IP, verified user, default port, region from the node",
			node: Node{
				ID: "n", PrivateIP: "10.0.0.7",
				ConnectionConfig: NodeConnConfig{ProviderName: "Azure"},
				Region:           NodeRegion{Region: "koreacentral", Zone: "1"},
			},
			key: key,
			want: NodeAccess{Host: "10.0.0.7", Port: 22, User: "verified", PrivateKey: testPrivateKey,
				CSP: "azure", Region: "koreacentral", Zone: "1"},
		},
		{
			name: "key user name when nothing was verified",
			node: Node{ID: "n", PublicIP: "198.51.100.1", SSHPort: 2222, ConnectionConfig: NodeConnConfig{ProviderName: "gcp"}},
			key:  &SSHKey{ID: "k", Username: "key-user", PrivateKey: testPrivateKey},
			want: NodeAccess{Host: "198.51.100.1", Port: 2222, User: "key-user", PrivateKey: testPrivateKey, CSP: "gcp"},
		},
		{name: "no address", node: Node{ID: "n"}, key: key, wantErr: "neither a public nor a private IP"},
		{name: "no user", node: Node{ID: "n", PublicIP: "198.51.100.1"}, key: &SSHKey{ID: "k", PrivateKey: testPrivateKey}, wantErr: "no user name"},
		{name: "no private key", node: Node{ID: "n", PublicIP: "198.51.100.1", NodeUserName: "u"}, key: &SSHKey{ID: "k"}, wantErr: "no private key"},
		{name: "no key", node: Node{ID: "n", PublicIP: "198.51.100.1"}, wantErr: "no ssh key"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveNodeAccess(&tc.node, tc.key)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %v, want one containing %q", err, tc.wantErr)
				}
				if strings.Contains(err.Error(), "c2VjcmV0LWtleS1ib2R5") {
					t.Errorf("error carries the private key: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveNodeAccess: %v", err)
			}
			if *got != tc.want {
				t.Errorf("access = %+v, want %+v", *got, tc.want)
			}
		})
	}
}

func TestGetNodeReadsTheSpec(t *testing.T) {
	c := newTestClient(newNodeServer(t))

	node, err := c.GetNode(context.Background(), "etri", "travel", "vm1")
	if err != nil || node.SpecID != "aws+ap-northeast-2+t3.small" {
		t.Errorf("GetNode = %+v, %v, want the spec id", node, err)
	}
}

func TestControlNode(t *testing.T) {
	c := newTestClient(newNodeServer(t))

	msg, err := c.ControlNode(context.Background(), "etri", "travel", "vm1", NodeActionReboot)
	if err != nil || msg != "reboot vm1 done" {
		t.Errorf("ControlNode = %q, %v", msg, err)
	}
	if _, err := c.ControlNode(context.Background(), "etri", "travel", "vm9", NodeActionReboot); err == nil ||
		!strings.Contains(err.Error(), `"vm9"`) {
		t.Errorf("ControlNode on a missing node = %v, want an error naming it", err)
	}
}
