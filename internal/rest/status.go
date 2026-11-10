package rest

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/ish-hcc/ai-agent-proto/internal/config"
	"github.com/ish-hcc/ai-agent-proto/internal/model"
	"github.com/ish-hcc/ai-agent-proto/internal/nodedeploy"
	"github.com/ish-hcc/ai-agent-proto/internal/redact"
	"github.com/ish-hcc/ai-agent-proto/internal/semaphore"
	"github.com/ish-hcc/ai-agent-proto/internal/tumblebug"
)

// statusCheckTimeout bounds each dependency check of GET /status, so that a
// dependency that does not answer cannot hold the response for the
// CB-Tumblebug client timeout, which is minutes.
const statusCheckTimeout = 5 * time.Second

// statusPlaybooks are the playbooks whose Semaphore templates GET /status
// looks for.
var statusPlaybooks = []string{
	nodedeploy.PlaybookInstallDocker,
	nodedeploy.PlaybookComposeUp,
	nodedeploy.PlaybookCopyAndRun,
	nodedeploy.PlaybookComposeDown,
}

// StatusChecker answers GET /status.
type StatusChecker struct {
	semaphore    *semaphore.Client
	tumblebug    *tumblebug.Client
	tumblebugURL string
	semaphoreURL string
	stageRoot    string
	dryRun       bool
	openPorts    bool
}

// NewStatusChecker builds the checker of the dependencies cfg names. sem and tb
// are the clients deployments use, so that the check goes through the same
// session and settings.
func NewStatusChecker(cfg *config.Config, sem *semaphore.Client, tb *tumblebug.Client) *StatusChecker {
	return &StatusChecker{
		semaphore:    sem,
		tumblebug:    tb,
		tumblebugURL: cfg.Tumblebug.BaseURL,
		semaphoreURL: cfg.Semaphore.URL,
		stageRoot:    cfg.Stage.Root,
		dryRun:       cfg.Tumblebug.DryRun,
		openPorts:    cfg.NodeDeploy.OpenPorts,
	}
}

// Check runs every check at once and waits for all of them.
func (s *StatusChecker) Check(ctx context.Context) model.ServiceStatus {
	status := model.ServiceStatus{DryRun: s.dryRun, OpenPorts: s.openPorts, CheckedAt: time.Now().UTC()}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		status.Semaphore = s.checkSemaphore(ctx)
	}()
	go func() {
		defer wg.Done()
		status.Tumblebug = s.checkTumblebug(ctx)
	}()
	status.Stage = checkStage(s.stageRoot)
	wg.Wait()
	return status
}

func (s *StatusChecker) checkSemaphore(ctx context.Context) model.SemaphoreStatus {
	ctx, cancel := context.WithTimeout(ctx, statusCheckTimeout)
	defer cancel()
	h := s.semaphore.Check(ctx, statusPlaybooks)

	out := model.SemaphoreStatus{
		DependencyStatus: model.DependencyStatus{Reachable: h.Reachable, Version: h.Version},
		Configured:       h.Configured,
		Authenticated:    h.Authenticated,
		Project:          h.Project,
		ProjectID:        h.ProjectID,
		Templates:        make([]model.SemaphoreTemplateStatus, 0, len(h.Templates)),
		UIURL:            h.UIURL,
	}
	if s.semaphoreURL != "" {
		out.URL = redact.URL(s.semaphoreURL)
	}
	if h.Reachable {
		out.LatencyMs = latencyMs(h.Latency)
	}
	missing := 0
	for _, t := range h.Templates {
		out.Templates = append(out.Templates, model.SemaphoreTemplateStatus{
			Name: t.Name, Playbook: t.Playbook, Exists: t.ID != 0, ID: t.ID,
		})
		if t.ID == 0 {
			missing++
		}
	}

	switch {
	case h.Err != nil:
		out.Message = redact.Error(h.Err, s.semaphoreURL)
	case h.ProjectID == 0:
		out.Message = fmt.Sprintf("ready; project %q does not exist yet, the first deployment creates it", h.Project)
	case missing > 0:
		out.Message = fmt.Sprintf("ready; %d of %d playbook templates do not exist yet, "+
			"the first deployment that runs each creates it", missing, len(h.Templates))
	default:
		out.Message = "ready"
	}
	if s.dryRun {
		out.Message += "; dry run is on, so deployments do not call Semaphore"
	}
	if h.UIURL == "" {
		out.Message += "; AIAPP_SEMAPHORE_UI_URL is not set, so deployment records carry no task links"
	}
	return out
}

func (s *StatusChecker) checkTumblebug(ctx context.Context) model.DependencyStatus {
	ctx, cancel := context.WithTimeout(ctx, statusCheckTimeout)
	defer cancel()
	out := model.DependencyStatus{URL: redact.URL(s.tumblebugURL)}

	var resp model.SimpleMsg
	start := time.Now()
	err := s.tumblebug.Do(ctx, http.MethodGet, "/readyz", nil, nil, &resp)
	elapsed := time.Since(start)
	var apiErr *tumblebug.APIError
	switch {
	case err == nil:
		out.Reachable = true
		out.LatencyMs = latencyMs(elapsed)
		out.Message = resp.Message
		if out.Message == "" {
			out.Message = "ready"
		}
	case errors.As(err, &apiErr):
		// It answered, so it is reachable, but not ready.
		out.Reachable = true
		out.LatencyMs = latencyMs(elapsed)
		out.Message = "not ready: " + redact.Error(err, s.tumblebugURL)
	default:
		out.Message = redact.Error(err, s.tumblebugURL)
	}
	return out
}

// checkStage creates and removes a file in root, the way a deployment places
// its files there.
func checkStage(root string) model.StageStatus {
	out := model.StageStatus{Path: root}
	info, err := os.Stat(root)
	switch {
	case errors.Is(err, os.ErrNotExist):
		out.Message = "does not exist; each deployment creates its folder under it, which needs the parent to be writable"
		return out
	case err != nil:
		out.Message = err.Error()
		return out
	case !info.IsDir():
		out.Message = "is not a directory"
		return out
	}
	f, err := os.CreateTemp(root, ".aiapp-status-*")
	if err != nil {
		out.Message = "not writable: " + err.Error()
		return out
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	out.Writable = true
	out.Message = "writable"
	return out
}

func latencyMs(d time.Duration) *int64 {
	ms := d.Milliseconds()
	return &ms
}

// RestGetStatus godoc
// @ID GetStatus
// @Summary Check the dependencies of deployment
// @Description Check, at the time of the call, what deploying onto nodes depends on, and answer 200 with what was found whether or not each dependency is up. Nothing is created or changed in any of them.
// @Description
// @Description semaphore: whether GET /api/ping answers (reachable, latencyMs), whether the configured token or user name and password are accepted (authenticated), the version from /api/info, whether the project (AIAPP_SEMAPHORE_PROJECT) exists and whether the template of each deployment playbook (aiapp-install-docker, aiapp-compose-up, aiapp-copy-and-run, aiapp-compose-down) exists. A project or template that does not exist yet is not a fault: the first deployment that needs it creates it. uiUrl is AIAPP_SEMAPHORE_UI_URL, the web UI that the taskUrl of each recorded playbook run points at; without it records carry no task links. tumblebug: whether CB-Tumblebug GET /readyz answers. stage: whether a file can be created in AIAPP_STAGE_ROOT. dryRun and openPorts are AIAPP_TUMBLEBUG_DRY_RUN and AIAPP_DEPLOYMENT_OPEN_PORTS. Each check gives up after 5 seconds. URLs are shown without credentials and no credential is part of the response.
// @Description
// @Description GET /readyz stays the liveness probe: it says only whether this service started, and calls nothing.
// @Tags [Health] Service Health
// @Produce json
// @Success 200 {object} model.ServiceStatus "What each dependency answered"
// @Security BearerAuth
// @Router /status [get]
func (h *Handler) RestGetStatus(c echo.Context) error {
	return c.JSON(http.StatusOK, h.status.Check(c.Request().Context()))
}
