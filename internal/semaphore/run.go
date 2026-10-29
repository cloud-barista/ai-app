package semaphore

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

const (
	// RepositoryName is the project repository that points at the playbook folder.
	RepositoryName = "aiapp-playbooks"
	// EnvironmentName is the empty environment every template is bound to. A
	// template without an environment cannot start a task in v2.12.17.
	EnvironmentName = "aiapp-empty"
	// PlaceholderInventoryName is the empty inventory every template is bound
	// to. v2.12.17 refuses a template without an inventory, and a template
	// bound to a run's own inventory would keep that inventory, and its key,
	// from being deleted. Tasks never run with it: each is started with the
	// inventory of its run.
	PlaceholderInventoryName = "aiapp-no-hosts"

	defaultPollInterval = 5 * time.Second
	defaultTimeout      = 30 * time.Minute
	// cleanupTimeout bounds the stop, output and delete calls made after the
	// caller's context is done, so that a cancelled run still cleans up.
	cleanupTimeout = time.Minute
	// stopPolls bounds how many times a stopped task is asked whether it ended.
	stopPolls = 10
	// outputPolls bounds how many more times the log of an ended task is read
	// while it still grows.
	outputPolls = 15
	// maxNameLength keeps generated resource names readable in the Semaphore UI.
	maxNameLength = 48

	redactedText = "<redacted>"
)

var (
	hostPattern     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.:_-]*$`)
	userPattern     = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]*$`)
	playbookPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]*\.ya?ml$`)
	namePattern     = regexp.MustCompile(`[^A-Za-z0-9._-]+`)
	ansiPattern     = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)
)

// RunSpec is one playbook run against one node.
type RunSpec struct {
	// Name labels the key and inventory registered for the run, for example
	// "<deployment id>-<csp>-<node id>". A random suffix keeps runs apart.
	Name string
	Host string
	// Port is the SSH port. Zero means 22.
	Port int
	User string
	// PrivateKey is registered for the run only and deleted afterwards. It is
	// masked out of the output and of every error.
	PrivateKey string
	// Playbook is the playbook file, relative to the playbook folder.
	Playbook string
	// ExtraVars are passed to the playbook as extra variables.
	ExtraVars map[string]any
}

// RunResult is the outcome of a Run.
type RunResult struct {
	// TaskID is zero when the task never started.
	TaskID int `json:"taskId"`
	// Status is the final Semaphore task status, or the last one seen when the
	// run timed out or was cancelled. Empty when the task never started.
	Status string `json:"status"`
	// Output is the task log, one line per entry, colour codes removed.
	Output []string `json:"output"`
	// CleanupErrors lists the registered resources that could not be deleted.
	// A key named here may still be stored in Semaphore.
	CleanupErrors []string `json:"cleanupErrors,omitempty"`
}

// Succeeded reports whether the task ended in success.
func (r *RunResult) Succeeded() bool { return r != nil && r.Status == StatusSuccess }

// Run runs a playbook against one node and waits for it to end.
//
// Each playbook has one template, named TemplateName(playbook), created on
// first use and kept, because Semaphore v2.12.17 deletes a template's tasks and
// their logs with the template. The node's key and inventory are registered
// for this run only: the task is started with the run's inventory, which
// replaces the template's placeholder inventory, and both are deleted
// afterwards, inventory first, whether the run succeeded, failed or was
// cancelled. The finished task and its log stay in Semaphore.
//
// The returned error covers failures to register, start, follow or read the
// task. A task that ran and failed is not an error: its Status says so. The
// result is never nil, so CleanupErrors is visible either way.
func (c *Client) Run(ctx context.Context, spec RunSpec) (result *RunResult, err error) {
	result = &RunResult{Output: []string{}}
	defer func() {
		if err != nil {
			err = errors.New(maskSecret(err.Error(), spec.PrivateKey))
		}
	}()

	if err := validateSpec(&spec); err != nil {
		return result, err
	}

	projectID, templateID, err := c.setup(ctx, spec.Playbook)
	if err != nil {
		return result, err
	}

	name, err := resourceName(spec.Name)
	if err != nil {
		return result, err
	}

	// Cleanups run in reverse: Semaphore refuses to delete a key an inventory
	// uses.
	var cleanups []func(context.Context) error
	defer func() {
		cctx, cancel := detached(ctx)
		defer cancel()
		for i := len(cleanups) - 1; i >= 0; i-- {
			if cerr := cleanups[i](cctx); cerr != nil {
				msg := maskSecret(cerr.Error(), spec.PrivateKey)
				result.CleanupErrors = append(result.CleanupErrors, msg)
				log.Warn().Str("run", name).Str("error", msg).Msg("Failed to clean up a Semaphore resource")
			}
		}
	}()

	keyID, err := c.CreateSSHKey(ctx, projectID, name, spec.User, spec.PrivateKey)
	if err != nil {
		return result, err
	}
	cleanups = append(cleanups, func(cctx context.Context) error { return c.DeleteKey(cctx, projectID, keyID) })

	inventoryID, err := c.CreateInventory(ctx, projectID, name, inventoryText(spec), keyID)
	if err != nil {
		return result, err
	}
	cleanups = append(cleanups, func(cctx context.Context) error { return c.DeleteInventory(cctx, projectID, inventoryID) })

	task, err := c.StartTask(ctx, projectID, templateID, inventoryID, spec.ExtraVars)
	if err != nil {
		return result, err
	}
	result.TaskID = task.ID
	result.Status = task.Status
	log.Info().Str("run", name).Int("task", task.ID).Str("playbook", spec.Playbook).Msg("Started Semaphore task")

	waitErr := c.wait(ctx, projectID, result)
	if waitErr != nil {
		// The task is stopped rather than left running with an inventory and a
		// key that are about to be deleted.
		cctx, cancel := detached(ctx)
		c.stop(cctx, projectID, result)
		cancel()
	}

	cctx, cancel := detached(ctx)
	defer cancel()
	lines, outErr := c.settledOutput(cctx, projectID, task.ID)
	if outErr == nil {
		result.Output = outputLines(lines, spec.PrivateKey)
	}
	log.Info().Str("run", name).Int("task", task.ID).Str("status", result.Status).Msg("Semaphore task ended")

	switch {
	case waitErr != nil:
		return result, waitErr
	case outErr != nil:
		return result, outErr
	}
	return result, nil
}

// TemplateName is the name of the template that runs playbook: "aiapp-" and
// the playbook path without its extension, for example aiapp-compose-up.
func TemplateName(playbook string) string {
	base := strings.TrimSuffix(strings.TrimSuffix(playbook, ".yml"), ".yaml")
	return "aiapp-" + strings.Trim(namePattern.ReplaceAllString(base, "-"), "-")
}

// setup finds or creates the project, its playbook repository, the empty
// environment, the placeholder inventory and the template of playbook, and
// returns the project and template IDs.
func (c *Client) setup(ctx context.Context, playbook string) (projectID, templateID int, err error) {
	c.setupMu.Lock()
	defer c.setupMu.Unlock()

	projectID, err = c.EnsureProject(ctx, c.project)
	if err != nil {
		return 0, 0, err
	}
	repositoryID, err := c.EnsureRepository(ctx, projectID, RepositoryName, c.playbookDir)
	if err != nil {
		return 0, 0, err
	}
	environmentID, err := c.EnsureEnvironment(ctx, projectID, EnvironmentName)
	if err != nil {
		return 0, 0, err
	}
	noneKeyID, err := c.EnsureNoneKey(ctx, projectID)
	if err != nil {
		return 0, 0, err
	}
	placeholderID, err := c.EnsureInventory(ctx, projectID, PlaceholderInventoryName, "", noneKeyID)
	if err != nil {
		return 0, 0, err
	}
	templateID, err = c.EnsureTemplate(ctx, projectID, Template{
		Name: TemplateName(playbook), Playbook: playbook,
		InventoryID: placeholderID, RepositoryID: repositoryID, EnvironmentID: environmentID,
	})
	if err != nil {
		return 0, 0, err
	}
	return projectID, templateID, nil
}

// wait polls the task until it ends, the run times out or ctx is done.
func (c *Client) wait(ctx context.Context, projectID int, result *RunResult) error {
	interval := c.pollInterval
	if interval <= 0 {
		interval = defaultPollInterval
	}
	timeout := c.timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	// The attempt cap backs up the deadline, so that a clock that does not
	// advance cannot keep the loop alive.
	maxPolls := int(timeout/interval) + 2

	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for i := 0; i < maxPolls; i++ {
		if IsFinished(result.Status) {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("semaphore task %d: %w", result.TaskID, ctx.Err())
		case <-deadline.C:
			return fmt.Errorf("semaphore task %d did not end within %s (last status %q)", result.TaskID, timeout, result.Status)
		case <-ticker.C:
		}
		task, err := c.GetTask(ctx, projectID, result.TaskID)
		if err != nil {
			if ctx.Err() != nil {
				return fmt.Errorf("semaphore task %d: %w", result.TaskID, ctx.Err())
			}
			return err
		}
		result.Status = task.Status
	}
	if IsFinished(result.Status) {
		return nil
	}
	return fmt.Errorf("semaphore task %d did not end within %d status checks (last status %q)", result.TaskID, maxPolls, result.Status)
}

// settledOutput reads the log of an ended task until two reads a poll apart
// return the same number of lines.
//
// Semaphore v2.12.17 stores log lines after the task status, not before it:
// a compose-up task whose last step printed about 2000 lines answered
// success with 186 of them stored, and the rest arrived within the second.
func (c *Client) settledOutput(ctx context.Context, projectID, taskID int) ([]OutputLine, error) {
	lines, err := c.TaskOutput(ctx, projectID, taskID)
	if err != nil {
		return nil, err
	}
	interval := c.pollInterval
	if interval <= 0 || interval > time.Second {
		interval = time.Second
	}
	for i := 0; i < outputPolls; i++ {
		select {
		case <-ctx.Done():
			return lines, nil
		case <-time.After(interval):
		}
		next, err := c.TaskOutput(ctx, projectID, taskID)
		if err != nil {
			return lines, nil
		}
		if len(next) == len(lines) {
			return next, nil
		}
		lines = next
	}
	return lines, nil
}

// stop asks Semaphore to stop the task and waits a bounded while for it to end.
func (c *Client) stop(ctx context.Context, projectID int, result *RunResult) {
	if IsFinished(result.Status) {
		return
	}
	if err := c.StopTask(ctx, projectID, result.TaskID); err != nil {
		log.Warn().Int("task", result.TaskID).Err(err).Msg("Failed to stop Semaphore task")
		return
	}
	interval := c.pollInterval
	if interval <= 0 || interval > time.Second {
		interval = time.Second
	}
	for i := 0; i < stopPolls; i++ {
		task, err := c.GetTask(ctx, projectID, result.TaskID)
		if err == nil {
			result.Status = task.Status
			if IsFinished(task.Status) {
				return
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}

// detached returns a context that outlives ctx's cancellation, bounded by cleanupTimeout.
func detached(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
}

func validateSpec(spec *RunSpec) error {
	if !hostPattern.MatchString(spec.Host) {
		return fmt.Errorf("invalid host %q", spec.Host)
	}
	if spec.Port == 0 {
		spec.Port = 22
	}
	if spec.Port < 1 || spec.Port > 65535 {
		return fmt.Errorf("invalid ssh port %d", spec.Port)
	}
	if !userPattern.MatchString(spec.User) {
		return fmt.Errorf("invalid ssh user %q", spec.User)
	}
	if err := CheckPrivateKey(spec.PrivateKey); err != nil {
		return err
	}
	if !playbookPattern.MatchString(spec.Playbook) || strings.Contains(spec.Playbook, "..") {
		return fmt.Errorf("invalid playbook %q", spec.Playbook)
	}
	return nil
}

// CheckPrivateKey reports why key is not a PEM private key block.
//
// Semaphore v2.12.17 does not refuse a malformed key when it is registered:
// the task that uses it makes the whole Semaphore process panic ("close of nil
// channel" in pkg/ssh/agent.go) and exit. A key is therefore checked here and
// never sent unless it decodes.
func CheckPrivateKey(key string) error {
	if strings.TrimSpace(key) == "" {
		return errors.New("no private key")
	}
	block, _ := pem.Decode([]byte(strings.TrimSpace(key)))
	if block == nil {
		return errors.New("the private key is not a PEM block")
	}
	if !strings.HasSuffix(block.Type, "PRIVATE KEY") {
		return fmt.Errorf("the PEM block is a %q, not a private key", block.Type)
	}
	return nil
}

// inventoryText is the static inventory of one node. Host key checking is off
// because the nodes are freshly created and their host keys are not known in
// advance; the key and the inventory live only for this run.
func inventoryText(spec RunSpec) string {
	return fmt.Sprintf("node ansible_host=%s ansible_port=%d ansible_user=%s ansible_python_interpreter=auto_silent "+
		"ansible_ssh_common_args='-o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null'\n",
		spec.Host, spec.Port, spec.User)
}

// resourceName builds "aiapp-<label>-<random>" from a caller label.
func resourceName(label string) (string, error) {
	suffix := make([]byte, 4)
	if _, err := rand.Read(suffix); err != nil {
		return "", fmt.Errorf("failed to generate a resource name: %w", err)
	}
	label = strings.Trim(namePattern.ReplaceAllString(label, "-"), "-")
	if label == "" {
		label = "run"
	}
	if len(label) > maxNameLength {
		label = label[:maxNameLength]
	}
	return "aiapp-" + label + "-" + hex.EncodeToString(suffix), nil
}

// outputLines flattens the task log into lines without colour codes, with the
// private key masked out.
func outputLines(entries []OutputLine, secret string) []string {
	lines := make([]string, 0, len(entries))
	for _, e := range entries {
		text := ansiPattern.ReplaceAllString(strings.TrimRight(e.Output, "\r\n"), "")
		text = maskSecret(text, secret)
		lines = append(lines, strings.Split(text, "\n")...)
	}
	return lines
}

// maskSecret removes secret from text, whole and line by line, so that a key
// quoted in part or with different line breaks is still removed. Lines shorter
// than 16 characters, such as the PEM armour, are left alone.
func maskSecret(text, secret string) string {
	secret = strings.TrimSpace(secret)
	if secret == "" || text == "" {
		return text
	}
	text = strings.ReplaceAll(text, secret, redactedText)
	for _, line := range strings.Split(secret, "\n") {
		line = strings.TrimSpace(line)
		if len(line) < 16 || strings.HasPrefix(line, "-----") {
			continue
		}
		text = strings.ReplaceAll(text, line, redactedText)
	}
	return text
}
