package nodedeploy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/ish-hcc/ai-agent-proto/internal/model"
	"github.com/ish-hcc/ai-agent-proto/internal/tumblebug"
)

// SSH user sources recorded on a node.
const (
	sshUserFromRequest   = "request"
	sshUserFromTumblebug = "tumblebug"
	sshUserFromFallback  = "fallback"
)

// sshProbeTimeout bounds one SSH login attempt, connection and authentication
// together.
const sshProbeTimeout = 10 * time.Second

// maxSSHProbes bounds the SSH login attempts made for one node, the
// CB-Tumblebug user included.
const maxSSHProbes = 6

// ErrSSHAuthRefused is wrapped by SSHProber.Probe when the SSH server answered
// and refused the user with the key.
var ErrSSHAuthRefused = errors.New("ssh authentication refused")

// SSHProber tells whether a user logs in to a node with a private key.
type SSHProber interface {
	// Probe tries public-key authentication as user on host:port. It returns
	// nil when the user authenticated, an error wrapping ErrSSHAuthRefused
	// when the server answered and refused the key for the user, and any
	// other error when the login could not be tried, such as a node that
	// does not answer yet.
	Probe(ctx context.Context, host string, port int, user, privateKey string) error
}

// osDefaultUsers holds the default login users of the cloud images, by
// operating system: the user an image creates for the key given at launch.
// An OS is matched when one of its names appears in the node image's osType,
// then osDistribution, then image name; the first entry that matches wins.
var osDefaultUsers = []struct {
	names []string
	users []string
}{
	{names: []string{"amazon linux", "amzn", "al2023"}, users: []string{"ec2-user"}},
	{names: []string{"red hat", "rhel"}, users: []string{"ec2-user"}},
	{names: []string{"rocky"}, users: []string{"rocky"}},
	{names: []string{"almalinux", "alma linux"}, users: []string{"almalinux", "ec2-user"}},
	{names: []string{"centos"}, users: []string{"centos"}},
	{names: []string{"fedora"}, users: []string{"fedora"}},
	{names: []string{"oracle", "ol"}, users: []string{"opc", "ec2-user"}},
	{names: []string{"debian"}, users: []string{"admin"}},
	{names: []string{"ubuntu"}, users: []string{"ubuntu"}},
	{names: []string{"suse", "sles"}, users: []string{"ec2-user"}},
}

// unknownOSUsers are tried when the node image names no known OS.
var unknownOSUsers = []string{"ec2-user", "ubuntu", "admin"}

// defaultUsersFor returns the default users of the image node runs, in the
// order to try them.
func defaultUsersFor(node *tumblebug.Node) []string {
	for _, text := range []string{node.Image.OSType, node.Image.OSDistribution, node.Image.CSPImageName, node.ImageID} {
		words := osWords(text)
		for _, entry := range osDefaultUsers {
			for _, name := range entry.names {
				if matchesOSName(text, words, name) {
					return entry.users
				}
			}
		}
	}
	return unknownOSUsers
}

// osWords splits text into lower case words of letters and digits.
func osWords(text string) []string {
	return strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	})
}

// matchesOSName reports whether name appears in text. A short name, such as
// ol, must be a word of its own, or a word that only adds a version (ol9).
func matchesOSName(text string, words []string, name string) bool {
	if len(name) > 2 {
		return strings.Contains(strings.ToLower(text), name)
	}
	for _, w := range words {
		if rest, ok := strings.CutPrefix(w, name); ok && strings.Trim(rest, "0123456789") == "" {
			return true
		}
	}
	return false
}

// decideSSHUser picks the SSH user the playbooks of a node run as, when the
// target gave none. It tries tbUser, the one CB-Tumblebug reports, first and
// keeps it when it logs in. When the node refuses it, the default users of
// the node image are tried in order and the first that logs in is taken. When
// a login cannot be tried at all, nothing is decided and tbUser is kept: the
// node may still be booting, and install-docker waits for it.
//
// It returns the user, its source and a note for the node, empty when there
// is nothing to note.
func (s *Service) decideSSHUser(ctx context.Context, access *tumblebug.NodeAccess, node *tumblebug.Node, tbUser string) (string, string, string) {
	probe := func(user string) error {
		return s.opts.SSHProber.Probe(ctx, access.Host, access.Port, user, access.PrivateKey)
	}
	err := probe(tbUser)
	if err == nil {
		return tbUser, sshUserFromTumblebug, ""
	}
	if !errors.Is(err, ErrSSHAuthRefused) {
		return tbUser, sshUserFromTumblebug, fmt.Sprintf("ssh user %s was not checked before the playbooks: %s", tbUser, err)
	}

	tried := []string{tbUser}
	for _, user := range defaultUsersFor(node) {
		if len(tried) >= maxSSHProbes {
			break
		}
		if user == tbUser {
			continue
		}
		err := probe(user)
		if err == nil {
			return user, sshUserFromFallback, fmt.Sprintf(
				"the node refused ssh user %s with the CB-Tumblebug key; the playbooks run as %s, which it accepted", tbUser, user)
		}
		tried = append(tried, user)
		if !errors.Is(err, ErrSSHAuthRefused) {
			return tbUser, sshUserFromTumblebug, fmt.Sprintf(
				"the node refused ssh user %s with the CB-Tumblebug key, and trying %s failed: %s; the playbooks run as %s",
				tbUser, user, err, tbUser)
		}
	}
	return tbUser, sshUserFromTumblebug, fmt.Sprintf(
		"the node refused every ssh user tried with the CB-Tumblebug key (%s); the playbooks run as %s; give targets[].sshUser to choose the user",
		strings.Join(tried, ", "), tbUser)
}

// sshUserSourceOf is the source of the user a dry run, or a target that gave
// sshUser, records.
func sshUserSourceOf(target model.NodeDeployTarget) string {
	if target.SSHUser != "" {
		return sshUserFromRequest
	}
	return sshUserFromTumblebug
}

// sshDialProber is the SSHProber that logs in to the node over the network.
type sshDialProber struct {
	timeout time.Duration
}

// NewSSHProber returns an SSHProber that connects to the node and tries
// public-key authentication, each attempt within timeout. It opens no session
// and runs no command. The host key is not checked, as the playbooks do not
// check it: the nodes are freshly created and their host keys are not known in
// advance.
func NewSSHProber(timeout time.Duration) SSHProber {
	if timeout <= 0 {
		timeout = sshProbeTimeout
	}
	return &sshDialProber{timeout: timeout}
}

func (p *sshDialProber) Probe(ctx context.Context, host string, port int, user, privateKey string) error {
	signer, err := ssh.ParsePrivateKey([]byte(privateKey))
	if err != nil {
		return fmt.Errorf("the private key cannot be used: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	addr := net.JoinHostPort(host, strconv.Itoa(port))
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Now()) })
	defer stop()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}

	offered := false
	config := &ssh.ClientConfig{
		User: user,
		Auth: []ssh.AuthMethod{ssh.PublicKeysCallback(func() ([]ssh.Signer, error) {
			offered = true
			return []ssh.Signer{signer}, nil
		})},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         p.timeout,
	}
	client, chans, reqs, err := ssh.NewClientConn(conn, addr, config)
	if err != nil {
		if offered && strings.Contains(err.Error(), "unable to authenticate") {
			return fmt.Errorf("%w: %s", ErrSSHAuthRefused, err)
		}
		return err
	}
	go ssh.DiscardRequests(reqs)
	go func() {
		for ch := range chans {
			_ = ch.Reject(ssh.Prohibited, "no channels")
		}
	}()
	_ = client.Close()
	return nil
}
