package nodedeploy

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/ish-hcc/ai-agent-proto/internal/model"
	"github.com/ish-hcc/ai-agent-proto/internal/tumblebug"
)

// fakeProber stands in for SSH logins. results maps "<host> <user>" to what
// Probe returns; a pair not listed returns otherwise, nil (logged in) unless
// set.
type fakeProber struct {
	mu        sync.Mutex
	results   map[string]error
	otherwise error
	calls     []string
}

func (f *fakeProber) Probe(ctx context.Context, host string, port int, user, privateKey string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, host+" "+user)
	if privateKey != testKey {
		return errors.New("probed with another key")
	}
	if err, ok := f.results[host+" "+user]; ok {
		return err
	}
	return f.otherwise
}

func (f *fakeProber) callsTo(host string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var users []string
	for _, c := range f.calls {
		if h, user, _ := strings.Cut(c, " "); h == host {
			users = append(users, user)
		}
	}
	return users
}

var errRefused = fmt.Errorf("%w: no supported methods remain", ErrSSHAuthRefused)

func nodeOf(t *testing.T, d *model.NodeDeployment, nodeID string) model.NodeDeployResult {
	t.Helper()
	for _, n := range d.Nodes {
		if n.Target.NodeID == nodeID {
			return n
		}
	}
	t.Fatalf("no node %s", nodeID)
	return model.NodeDeployResult{}
}

func assertRanAs(t *testing.T, n model.NodeDeployResult, user string) {
	t.Helper()
	if len(n.Steps) == 0 {
		t.Errorf("%s: no steps", n.Target.NodeID)
	}
	for _, st := range n.Steps {
		if st.SSHUser != user {
			t.Errorf("%s: step %s as %q, want %q", n.Target.NodeID, st.Playbook, st.SSHUser, user)
		}
	}
}

func TestSSHUserOfCBTumblebugAcceptedIsKept(t *testing.T) {
	f := newFixture(t, false)
	d := deployed(t, f, appReq("vm1"))
	n := nodeOf(t, d, "vm1")
	if n.Status != model.NodeDeployRunning || n.SSHUser != "cb-user" || n.SSHUserSource != sshUserFromTumblebug || len(n.Notes) != 0 {
		t.Errorf("node = %q user %q source %q notes %v", n.Status, n.SSHUser, n.SSHUserSource, n.Notes)
	}
	assertRanAs(t, n, "cb-user")
	if got := f.prober.callsTo("203.0.113.11"); !slices.Equal(got, []string{"cb-user"}) {
		t.Errorf("probes = %v, want cb-user only", got)
	}
}

func TestSSHUserFallsBackToTheFirstImageUserAccepted(t *testing.T) {
	f := newFixture(t, false)
	f.prober.otherwise = errRefused
	f.prober.results["203.0.113.11 ec2-user"] = nil
	f.prober.results["198.51.100.12 ubuntu"] = nil
	d := deployed(t, f, appReq("vm1", "vm2"))

	want := map[string]struct {
		user   string
		probes []string
	}{
		"vm1": {"ec2-user", []string{"cb-user", "ec2-user"}},
		"vm2": {"ubuntu", []string{"cb-user", "ec2-user", "ubuntu"}},
	}
	for _, n := range d.Nodes {
		w := want[n.Target.NodeID]
		if n.Status != model.NodeDeployRunning || n.SSHUser != w.user || n.SSHUserSource != sshUserFromFallback {
			t.Errorf("%s = %q user %q source %q, want running as %s from fallback",
				n.Target.NodeID, n.Status, n.SSHUser, n.SSHUserSource, w.user)
		}
		if n.Node == nil || n.Node.User != "cb-user" {
			t.Errorf("%s: node %+v, want node.user cb-user", n.Target.NodeID, n.Node)
		}
		if !slices.ContainsFunc(n.Notes, func(s string) bool {
			return strings.Contains(s, "refused ssh user cb-user") && strings.Contains(s, "run as "+w.user)
		}) {
			t.Errorf("%s: notes %v, want one naming cb-user and %s", n.Target.NodeID, n.Notes, w.user)
		}
		assertRanAs(t, n, w.user)
		if got := f.prober.callsTo(n.Node.Host); !slices.Equal(got, w.probes) {
			t.Errorf("%s: probes = %v, want %v", n.Target.NodeID, got, w.probes)
		}
	}

	before := f.runner.count()
	if _, err := f.svc.Remove(context.Background(), d.ID); err != nil {
		t.Fatal(err)
	}
	if done := waitRemoved(t, f.svc, d.ID); done.Status != model.NodeDeployRemoved {
		t.Fatalf("remove = %q", done.Status)
	}
	for _, run := range downRuns(f, before) {
		w := want["vm2"].user
		if strings.Contains(run.Name, "-vm1") {
			w = want["vm1"].user
		}
		if run.User != w {
			t.Errorf("compose-down %s as %q, want %q", run.Name, run.User, w)
		}
	}
	if got := len(f.prober.callsTo("203.0.113.11")); got != 2 {
		t.Errorf("probes after removal = %d, want no new one", got)
	}
}

func TestSSHUserEveryUserRefusedKeepsTheCBTumblebugUser(t *testing.T) {
	f := newFixture(t, false)
	f.prober.otherwise = errRefused
	d := deployed(t, f, appReq("vm1"))
	n := nodeOf(t, d, "vm1")
	if n.SSHUser != "cb-user" || n.SSHUserSource != sshUserFromTumblebug {
		t.Errorf("user %q source %q, want cb-user from tumblebug", n.SSHUser, n.SSHUserSource)
	}
	if len(n.Notes) != 1 || !strings.Contains(n.Notes[0], "refused every ssh user tried") ||
		!strings.Contains(n.Notes[0], "cb-user, ec2-user") {
		t.Errorf("notes = %v", n.Notes)
	}
	assertRanAs(t, n, "cb-user")
	if got := f.prober.callsTo("203.0.113.11"); !slices.Equal(got, []string{"cb-user", "ec2-user"}) {
		t.Errorf("probes = %v", got)
	}
}

func TestSSHUserUnreachableNodeKeepsTheCBTumblebugUser(t *testing.T) {
	f := newFixture(t, false)
	f.prober.otherwise = errors.New("dial tcp 203.0.113.11:22: connect: connection refused")
	d := deployed(t, f, appReq("vm1"))
	n := nodeOf(t, d, "vm1")
	if n.Status != model.NodeDeployRunning || n.SSHUser != "cb-user" || n.SSHUserSource != sshUserFromTumblebug {
		t.Errorf("node = %q user %q source %q", n.Status, n.SSHUser, n.SSHUserSource)
	}
	if len(n.Notes) != 1 || !strings.Contains(n.Notes[0], "was not checked") || !strings.Contains(n.Notes[0], "connection refused") {
		t.Errorf("notes = %v", n.Notes)
	}
	assertRanAs(t, n, "cb-user")
	if got := f.prober.callsTo("203.0.113.11"); !slices.Equal(got, []string{"cb-user"}) {
		t.Errorf("probes = %v, want cb-user only", got)
	}
}

func TestSSHUserOfTheRequestIsNotProbed(t *testing.T) {
	f := newFixture(t, false)
	f.prober.otherwise = errRefused
	req := appReq("vm1")
	req.Targets[0].SSHUser = "ec2-user"
	d := deployed(t, f, req)
	n := nodeOf(t, d, "vm1")
	if n.SSHUser != "ec2-user" || n.SSHUserSource != sshUserFromRequest || len(n.Notes) != 0 {
		t.Errorf("user %q source %q notes %v", n.SSHUser, n.SSHUserSource, n.Notes)
	}
	assertRanAs(t, n, "ec2-user")
	if got := f.prober.callsTo("203.0.113.11"); len(got) != 0 {
		t.Errorf("probes = %v, want none", got)
	}
}

func TestSSHUserIsNotProbedInADryRun(t *testing.T) {
	f := newFixture(t, true)
	d, err := f.svc.Create(context.Background(), modelReq("vm1"))
	if err != nil {
		t.Fatal(err)
	}
	n := d.Nodes[0]
	if d.Status != model.NodeDeployDryRun || n.SSHUser != "cb-user" || n.SSHUserSource != sshUserFromTumblebug {
		t.Errorf("dry run = %q user %q source %q", d.Status, n.SSHUser, n.SSHUserSource)
	}
	if got := f.prober.callsTo("203.0.113.11"); len(got) != 0 {
		t.Errorf("probes = %v, want none", got)
	}
}

func TestDefaultUsersFor(t *testing.T) {
	cases := []struct {
		name  string
		image tumblebug.NodeImage
		id    string
		want  []string
	}{
		{"amazon linux", tumblebug.NodeImage{OSType: "Amazon Linux 2023",
			OSDistribution: "al2023-ami-2023.12.20261102.0-kernel-6.18-x86_64"}, "", []string{"ec2-user"}},
		{"amazon linux gpu ami", tumblebug.NodeImage{OSType: "Amazon Linux 2023",
			OSDistribution: "Deep Learning Base OSS Nvidia Driver GPU AMI (Amazon Linux 2023) 20261027"}, "", []string{"ec2-user"}},
		{"rhel", tumblebug.NodeImage{OSType: "Red Hat Enterprise Linux 9",
			OSDistribution: "RHEL-9.8.0_HVM_GA-20260521-x86_64-0-Hourly2-GP3"}, "", []string{"ec2-user"}},
		{"almalinux", tumblebug.NodeImage{OSType: "AlmaLinux 9",
			OSDistribution: "almalinux:almalinux-x86_64:9-gen2:9.8.2026110201"}, "", []string{"almalinux", "ec2-user"}},
		{"rocky", tumblebug.NodeImage{OSType: "Rocky Linux 9",
			OSDistribution: "ciq:rocky-community-editions:rocky-community-9:9.7.20251123"}, "", []string{"rocky"}},
		{"centos", tumblebug.NodeImage{OSType: "CentOS Stream 9"}, "", []string{"centos"}},
		{"fedora", tumblebug.NodeImage{OSType: "Fedora 40"}, "", []string{"fedora"}},
		{"oracle", tumblebug.NodeImage{OSType: "Oracle Linux 9"}, "", []string{"opc", "ec2-user"}},
		{"ol in the distribution", tumblebug.NodeImage{OSDistribution: "OL9.4-x86_64-HVM-2024-06-17"}, "", []string{"opc", "ec2-user"}},
		{"debian", tumblebug.NodeImage{OSType: "Debian 12"}, "", []string{"admin"}},
		{"ubuntu", tumblebug.NodeImage{OSType: "Ubuntu 22.04"}, "", []string{"ubuntu"}},
		{"suse", tumblebug.NodeImage{OSType: "SUSE Linux Enterprise Server 15"}, "", []string{"ec2-user"}},
		{"os type before distribution", tumblebug.NodeImage{OSType: "Rocky Linux 9", OSDistribution: "ubuntu-like"}, "", []string{"rocky"}},
		{"only the image id", tumblebug.NodeImage{}, "debian-12-bookworm-v20261101", []string{"admin"}},
		{"unknown", tumblebug.NodeImage{OSType: "Windows Server 2022", OSDistribution: "tool console"}, "", []string{"ec2-user", "ubuntu", "admin"}},
		{"nothing", tumblebug.NodeImage{}, "", []string{"ec2-user", "ubuntu", "admin"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := defaultUsersFor(&tumblebug.Node{Image: c.image, ImageID: c.id})
			if !slices.Equal(got, c.want) {
				t.Errorf("users = %v, want %v", got, c.want)
			}
			if slices.Contains(got, "root") {
				t.Errorf("users %v hold root", got)
			}
		})
	}
}

// testSSHServer serves SSH on a local port and lets user in with the public
// key of key. It counts the channels clients asked for.
type testSSHServer struct {
	addr     *net.TCPAddr
	channels atomic.Int32
	logins   atomic.Int32
}

func newTestSSHServer(t *testing.T, user string, key ed25519.PrivateKey) *testSSHServer {
	t.Helper()
	_, hostPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	hostSigner, err := ssh.NewSignerFromKey(hostPriv)
	if err != nil {
		t.Fatal(err)
	}
	allowed, err := ssh.NewPublicKey(key.Public())
	if err != nil {
		t.Fatal(err)
	}
	srv := &testSSHServer{}
	config := &ssh.ServerConfig{
		PublicKeyCallback: func(meta ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
			if meta.User() == user && string(k.Marshal()) == string(allowed.Marshal()) {
				srv.logins.Add(1)
				return &ssh.Permissions{}, nil
			}
			return nil, errors.New("not allowed")
		},
	}
	config.AddHostKey(hostSigner)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	srv.addr = ln.Addr().(*net.TCPAddr)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				sconn, chans, reqs, err := ssh.NewServerConn(conn, config)
				if err != nil {
					return
				}
				defer sconn.Close()
				go ssh.DiscardRequests(reqs)
				for ch := range chans {
					srv.channels.Add(1)
					_ = ch.Reject(ssh.Prohibited, "no channels")
				}
			}()
		}
	}()
	return srv
}

func TestSSHDialProberOnlyAuthenticates(t *testing.T) {
	_, clientPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(clientPriv, "")
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := string(pem.EncodeToMemory(block))
	srv := newTestSSHServer(t, "ec2-user", clientPriv)
	prober := NewSSHProber(5 * time.Second)
	ctx := context.Background()
	host, port := srv.addr.IP.String(), srv.addr.Port

	if err := prober.Probe(ctx, host, port, "ec2-user", keyPEM); err != nil {
		t.Errorf("accepted user: %v", err)
	}
	if err := prober.Probe(ctx, host, port, "cb-user", keyPEM); !errors.Is(err, ErrSSHAuthRefused) {
		t.Errorf("refused user: err = %v, want ErrSSHAuthRefused", err)
	}
	_, otherPriv, _ := ed25519.GenerateKey(rand.Reader)
	otherBlock, _ := ssh.MarshalPrivateKey(otherPriv, "")
	if err := prober.Probe(ctx, host, port, "ec2-user", string(pem.EncodeToMemory(otherBlock))); !errors.Is(err, ErrSSHAuthRefused) {
		t.Errorf("other key: err = %v, want ErrSSHAuthRefused", err)
	}
	if got := srv.logins.Load(); got != 1 {
		t.Errorf("logins = %d, want 1", got)
	}

	// A login, once accepted, is closed at once: give the server a moment to
	// see a channel request if the prober made one.
	time.Sleep(100 * time.Millisecond)
	if got := srv.channels.Load(); got != 0 {
		t.Errorf("channels opened = %d, want none", got)
	}

	if err := prober.Probe(ctx, host, port, "ec2-user", "not a key"); err == nil || errors.Is(err, ErrSSHAuthRefused) {
		t.Errorf("bad key: err = %v, want an error that is not a refusal", err)
	}

	closed, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closedPort := closed.Addr().(*net.TCPAddr).Port
	_ = closed.Close()
	if err := prober.Probe(ctx, "127.0.0.1", closedPort, "ec2-user", keyPEM); err == nil || errors.Is(err, ErrSSHAuthRefused) {
		t.Errorf("closed port: err = %v, want an error that is not a refusal", err)
	}

	hangUp, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = hangUp.Close() })
	go func() {
		for {
			conn, err := hangUp.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	hangUpPort := hangUp.Addr().(*net.TCPAddr).Port
	if err := prober.Probe(ctx, "127.0.0.1", hangUpPort, "ec2-user", keyPEM); err == nil || errors.Is(err, ErrSSHAuthRefused) {
		t.Errorf("server hanging up: err = %v, want an error that is not a refusal", err)
	}

	silent, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = silent.Close() })
	silentPort := silent.Addr().(*net.TCPAddr).Port
	start := time.Now()
	err = NewSSHProber(300*time.Millisecond).Probe(ctx, "127.0.0.1", silentPort, "ec2-user", keyPEM)
	if err == nil || errors.Is(err, ErrSSHAuthRefused) {
		t.Errorf("silent server: err = %v, want a timeout", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("silent server took %s, want the timeout", elapsed)
	}
}
