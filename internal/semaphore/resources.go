package semaphore

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Key types and inventory types accepted by Semaphore.
const (
	keyTypeNone     = "none"
	keyTypeSSH      = "ssh"
	inventoryStatic = "static"
	appAnsible      = "ansible"
)

// Task statuses reported by Semaphore (pkg/task_logger in v2.12).
const (
	StatusWaiting  = "waiting"
	StatusStarting = "starting"
	StatusRunning  = "running"
	StatusStopping = "stopping"
	StatusStopped  = "stopped"
	StatusSuccess  = "success"
	StatusError    = "error"
	// StatusRejected ends a task that waited for a confirmation that was refused.
	StatusRejected = "rejected"
)

// IsFinished reports whether status is final. Semaphore itself counts
// stopped, success and error; rejected is final too, since nothing resumes it.
func IsFinished(status string) bool {
	switch status {
	case StatusStopped, StatusSuccess, StatusError, StatusRejected:
		return true
	}
	return false
}

// Project is a Semaphore project.
type Project struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// Repository is where a project's playbooks are read from. A local path is
// accepted as git_url.
type Repository struct {
	ID        int    `json:"id"`
	Name      string `json:"name"`
	ProjectID int    `json:"project_id"`
	GitURL    string `json:"git_url"`
	GitBranch string `json:"git_branch"`
	SSHKeyID  int    `json:"ssh_key_id"`
}

// Environment holds the default extra variables of a template. A task cannot
// start from a template without one: Semaphore v2.12.17 answers 500
// ("unexpected end of JSON input") and leaves the task in error.
type Environment struct {
	ID        int    `json:"id"`
	Name      string `json:"name"`
	ProjectID int    `json:"project_id"`
	JSON      string `json:"json"`
}

// AccessKey is a Semaphore key as listed. Semaphore never returns secrets.
type AccessKey struct {
	ID        int    `json:"id"`
	Name      string `json:"name"`
	Type      string `json:"type"`
	ProjectID int    `json:"project_id"`
}

// accessKeyRequest creates a key. Only the ssh variant is used here.
type accessKeyRequest struct {
	Name      string     `json:"name"`
	Type      string     `json:"type"`
	ProjectID int        `json:"project_id"`
	SSH       *sshSecret `json:"ssh,omitempty"`
}

type sshSecret struct {
	Login      string `json:"login"`
	Passphrase string `json:"passphrase"`
	PrivateKey string `json:"private_key"`
}

// Inventory is a static inventory with the key used to log in to its hosts.
type Inventory struct {
	ID        int    `json:"id"`
	Name      string `json:"name"`
	ProjectID int    `json:"project_id"`
	Inventory string `json:"inventory"`
	SSHKeyID  int    `json:"ssh_key_id"`
	Type      string `json:"type"`
}

// Template is a playbook bound to an inventory, a repository and an environment.
type Template struct {
	ID            int    `json:"id"`
	Name          string `json:"name"`
	ProjectID     int    `json:"project_id"`
	Playbook      string `json:"playbook"`
	InventoryID   int    `json:"inventory_id"`
	RepositoryID  int    `json:"repository_id"`
	EnvironmentID int    `json:"environment_id"`
	App           string `json:"app"`
}

// Task is one run of a template.
type Task struct {
	ID         int    `json:"id"`
	TemplateID int    `json:"template_id"`
	Status     string `json:"status"`
	// Environment is the extra variables of the run, as a JSON string.
	Environment string `json:"environment"`
	Message     string `json:"message"`
}

// taskRequest starts a task. InventoryID, when set, overrides the template's
// inventory for this run; v2.12.17 honours it even when the template does not
// allow argument overrides.
type taskRequest struct {
	TemplateID  int    `json:"template_id"`
	Environment string `json:"environment,omitempty"`
	InventoryID int    `json:"inventory_id,omitempty"`
}

// OutputLine is one line of a task log.
type OutputLine struct {
	TaskID int       `json:"task_id"`
	Time   time.Time `json:"time"`
	Output string    `json:"output"`
}

func projectPath(projectID int, rest string) string {
	return fmt.Sprintf("/api/project/%d/%s", projectID, rest)
}

// EnsureProject returns the ID of the project named name, creating it when
// none exists.
func (c *Client) EnsureProject(ctx context.Context, name string) (int, error) {
	var projects []Project
	if err := c.Do(ctx, http.MethodGet, "/api/projects", nil, &projects); err != nil {
		return 0, fmt.Errorf("failed to list semaphore projects: %w", err)
	}
	for _, p := range projects {
		if p.Name == name {
			return p.ID, nil
		}
	}
	created := &Project{}
	if err := c.Do(ctx, http.MethodPost, "/api/projects", map[string]any{"name": name}, created); err != nil {
		return 0, fmt.Errorf("failed to create semaphore project %q: %w", name, err)
	}
	return created.ID, nil
}

// EnsureNoneKey returns the ID of a key of type none, which a local
// repository needs, creating one when the project has none.
func (c *Client) EnsureNoneKey(ctx context.Context, projectID int) (int, error) {
	var keys []AccessKey
	if err := c.Do(ctx, http.MethodGet, projectPath(projectID, "keys"), nil, &keys); err != nil {
		return 0, fmt.Errorf("failed to list semaphore keys: %w", err)
	}
	for _, k := range keys {
		if k.Type == keyTypeNone {
			return k.ID, nil
		}
	}
	created := &AccessKey{}
	req := accessKeyRequest{Name: "None", Type: keyTypeNone, ProjectID: projectID}
	if err := c.Do(ctx, http.MethodPost, projectPath(projectID, "keys"), req, created); err != nil {
		return 0, fmt.Errorf("failed to create the semaphore none key: %w", err)
	}
	return created.ID, nil
}

// EnsureRepository returns the ID of the repository named name reading from
// path, creating it when it does not exist.
func (c *Client) EnsureRepository(ctx context.Context, projectID int, name, path string) (int, error) {
	var repos []Repository
	if err := c.Do(ctx, http.MethodGet, projectPath(projectID, "repositories"), nil, &repos); err != nil {
		return 0, fmt.Errorf("failed to list semaphore repositories: %w", err)
	}
	for _, r := range repos {
		if r.Name == name && r.GitURL == path {
			return r.ID, nil
		}
	}
	noneKeyID, err := c.EnsureNoneKey(ctx, projectID)
	if err != nil {
		return 0, err
	}
	created := &Repository{}
	req := Repository{Name: name, ProjectID: projectID, GitURL: path, SSHKeyID: noneKeyID}
	if err := c.Do(ctx, http.MethodPost, projectPath(projectID, "repositories"), req, created); err != nil {
		return 0, fmt.Errorf("failed to create semaphore repository %q: %w", name, err)
	}
	return created.ID, nil
}

// EnsureEnvironment returns the ID of the environment named name, creating
// an empty one when it does not exist.
func (c *Client) EnsureEnvironment(ctx context.Context, projectID int, name string) (int, error) {
	var envs []Environment
	if err := c.Do(ctx, http.MethodGet, projectPath(projectID, "environment"), nil, &envs); err != nil {
		return 0, fmt.Errorf("failed to list semaphore environments: %w", err)
	}
	for _, e := range envs {
		if e.Name == name {
			return e.ID, nil
		}
	}
	created := &Environment{}
	req := Environment{Name: name, ProjectID: projectID, JSON: "{}"}
	if err := c.Do(ctx, http.MethodPost, projectPath(projectID, "environment"), req, created); err != nil {
		return 0, fmt.Errorf("failed to create semaphore environment %q: %w", name, err)
	}
	return created.ID, nil
}

// CreateSSHKey registers a private key. The key is sent once and Semaphore
// never returns it.
func (c *Client) CreateSSHKey(ctx context.Context, projectID int, name, login, privateKey string) (int, error) {
	created := &AccessKey{}
	req := accessKeyRequest{
		Name: name, Type: keyTypeSSH, ProjectID: projectID,
		SSH: &sshSecret{Login: login, PrivateKey: privateKey},
	}
	if err := c.Do(ctx, http.MethodPost, projectPath(projectID, "keys"), req, created); err != nil {
		return 0, fmt.Errorf("failed to create semaphore key %q: %w", name, err)
	}
	return created.ID, nil
}

// DeleteKey removes a key. Semaphore refuses while an inventory uses it.
func (c *Client) DeleteKey(ctx context.Context, projectID, keyID int) error {
	if err := c.Do(ctx, http.MethodDelete, projectPath(projectID, fmt.Sprintf("keys/%d", keyID)), nil, nil); err != nil {
		return fmt.Errorf("failed to delete semaphore key %d: %w", keyID, err)
	}
	return nil
}

// CreateInventory registers a static inventory logged in to with keyID.
func (c *Client) CreateInventory(ctx context.Context, projectID int, name, text string, keyID int) (int, error) {
	created := &Inventory{}
	req := Inventory{Name: name, ProjectID: projectID, Inventory: text, SSHKeyID: keyID, Type: inventoryStatic}
	if err := c.Do(ctx, http.MethodPost, projectPath(projectID, "inventory"), req, created); err != nil {
		return 0, fmt.Errorf("failed to create semaphore inventory %q: %w", name, err)
	}
	return created.ID, nil
}

// DeleteInventory removes an inventory. Semaphore refuses while a template uses it.
func (c *Client) DeleteInventory(ctx context.Context, projectID, inventoryID int) error {
	if err := c.Do(ctx, http.MethodDelete, projectPath(projectID, fmt.Sprintf("inventory/%d", inventoryID)), nil, nil); err != nil {
		return fmt.Errorf("failed to delete semaphore inventory %d: %w", inventoryID, err)
	}
	return nil
}

// CreateTemplate registers an ansible template.
func (c *Client) CreateTemplate(ctx context.Context, projectID int, tpl Template) (int, error) {
	tpl.ProjectID = projectID
	if tpl.App == "" {
		tpl.App = appAnsible
	}
	created := &Template{}
	if err := c.Do(ctx, http.MethodPost, projectPath(projectID, "templates"), tpl, created); err != nil {
		return 0, fmt.Errorf("failed to create semaphore template %q: %w", tpl.Name, err)
	}
	return created.ID, nil
}

// DeleteTemplate removes a template. Semaphore deletes its tasks and their
// logs with it.
func (c *Client) DeleteTemplate(ctx context.Context, projectID, templateID int) error {
	if err := c.Do(ctx, http.MethodDelete, projectPath(projectID, fmt.Sprintf("templates/%d", templateID)), nil, nil); err != nil {
		return fmt.Errorf("failed to delete semaphore template %d: %w", templateID, err)
	}
	return nil
}

// StartTask runs a template with extraVars as its extra variables.
func (c *Client) StartTask(ctx context.Context, projectID, templateID int, extraVars map[string]any) (*Task, error) {
	req := taskRequest{TemplateID: templateID}
	if len(extraVars) > 0 {
		encoded, err := json.Marshal(extraVars)
		if err != nil {
			return nil, fmt.Errorf("failed to encode extra vars: %w", err)
		}
		req.Environment = string(encoded)
	}
	task := &Task{}
	if err := c.Do(ctx, http.MethodPost, projectPath(projectID, "tasks"), req, task); err != nil {
		return nil, fmt.Errorf("failed to start a task of semaphore template %d: %w", templateID, err)
	}
	return task, nil
}

// GetTask reads the status of a task.
func (c *Client) GetTask(ctx context.Context, projectID, taskID int) (*Task, error) {
	task := &Task{}
	if err := c.Do(ctx, http.MethodGet, projectPath(projectID, fmt.Sprintf("tasks/%d", taskID)), nil, task); err != nil {
		return nil, fmt.Errorf("failed to get semaphore task %d: %w", taskID, err)
	}
	return task, nil
}

// StopTask asks Semaphore to stop a running task.
func (c *Client) StopTask(ctx context.Context, projectID, taskID int) error {
	if err := c.Do(ctx, http.MethodPost, projectPath(projectID, fmt.Sprintf("tasks/%d/stop", taskID)), map[string]any{}, nil); err != nil {
		return fmt.Errorf("failed to stop semaphore task %d: %w", taskID, err)
	}
	return nil
}

// TaskOutput reads the log of a task, oldest line first.
func (c *Client) TaskOutput(ctx context.Context, projectID, taskID int) ([]OutputLine, error) {
	var lines []OutputLine
	if err := c.Do(ctx, http.MethodGet, projectPath(projectID, fmt.Sprintf("tasks/%d/output", taskID)), nil, &lines); err != nil {
		return nil, fmt.Errorf("failed to get the output of semaphore task %d: %w", taskID, err)
	}
	return lines, nil
}
