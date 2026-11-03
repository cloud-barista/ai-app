package semaphore

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// Health is what Check found. It holds no credentials.
type Health struct {
	// Configured is false when no Semaphore URL is set; nothing is called then.
	Configured bool
	// Reachable is whether GET /api/ping answered.
	Reachable bool
	// Latency is how long GET /api/ping took. Zero when it did not answer.
	Latency time.Duration
	// Authenticated is whether the configured token, or user name and
	// password, were accepted.
	Authenticated bool
	// Version is the Semaphore version from GET /api/info.
	Version string
	// Project is the configured project name.
	Project string
	// ProjectID is the project's ID, zero when it does not exist yet.
	ProjectID int
	// Templates are the templates of the given playbooks, in the given order.
	Templates []TemplateHealth
	// UIURL is the configured web UI address, empty when none is set.
	UIURL string
	// Err is the first check that failed, nil when every check passed. A
	// project or template that does not exist yet is not an error: the first
	// deployment creates it.
	Err error
}

// TemplateHealth says whether the template of one playbook exists.
type TemplateHealth struct {
	Name     string
	Playbook string
	// ID is zero when the template does not exist yet.
	ID int
}

// Check reads, without creating anything, whether Semaphore answers, whether
// the configured credentials are accepted, its version, and whether the
// project and the template of each playbook exist.
func (c *Client) Check(ctx context.Context, playbooks []string) Health {
	h := Health{Configured: c.baseURL != "", Project: c.project, UIURL: c.uiURL}
	for _, playbook := range playbooks {
		h.Templates = append(h.Templates, TemplateHealth{Name: TemplateName(playbook), Playbook: playbook})
	}
	if !h.Configured {
		h.Err = ErrNotConfigured
		return h
	}

	// The ping route needs no session, so a wrong password does not read as
	// an unreachable Semaphore.
	start := time.Now()
	if err := c.send(ctx, http.MethodGet, "/api/ping", nil, nil); err != nil {
		h.Err = err
		return h
	}
	h.Latency = time.Since(start)
	h.Reachable = true

	var info struct {
		Version string `json:"version"`
	}
	if err := c.Do(ctx, http.MethodGet, "/api/info", nil, &info); err != nil {
		switch {
		case errors.Is(err, ErrNoCredentials):
			h.Err = err
		case StatusCode(err) == http.StatusUnauthorized:
			h.Err = fmt.Errorf("semaphore refused the configured credentials: %w", err)
		default:
			h.Err = fmt.Errorf("failed to read semaphore info: %w", err)
		}
		return h
	}
	h.Authenticated = true
	h.Version = info.Version

	var projects []Project
	if err := c.Do(ctx, http.MethodGet, "/api/projects", nil, &projects); err != nil {
		h.Err = fmt.Errorf("failed to list semaphore projects: %w", err)
		return h
	}
	for _, p := range projects {
		if p.Name == c.project {
			h.ProjectID = p.ID
			break
		}
	}
	if h.ProjectID == 0 {
		return h
	}

	var templates []Template
	if err := c.Do(ctx, http.MethodGet, projectPath(h.ProjectID, "templates"), nil, &templates); err != nil {
		h.Err = fmt.Errorf("failed to list semaphore templates: %w", err)
		return h
	}
	for i := range h.Templates {
		for _, t := range templates {
			if t.Name == h.Templates[i].Name {
				h.Templates[i].ID = t.ID
				break
			}
		}
	}
	return h
}
