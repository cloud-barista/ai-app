package deploy

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/ish-hcc/ai-agent-proto/internal/model"
	"github.com/ish-hcc/ai-agent-proto/internal/tumblebug"
)

// Serving health wait defaults, used when no option replaces them.
const (
	defaultHealthTimeout  = 15 * time.Minute
	defaultHealthInterval = 20 * time.Second
)

// healthTimeoutMinutes bounds one remote health check. curl itself gives up
// after healthCurlMaxTime seconds, so the rest is SSH setup.
const healthTimeoutMinutes = 1

// healthCurlMaxTime is how long curl waits for the application, in seconds.
const healthCurlMaxTime = 5

// healthNoCurl is printed instead of a status code when the node has no curl.
// It is not a number, so it can never be read as an HTTP answer.
const healthNoCurl = "AIAPP-HEALTH-NO-CURL"

// errNoCurl is the per-node error reported when the node cannot run the check.
const errNoCurl = "curl not found on node"

// ErrNoAppLabel reports that an Infra carries no application label, so nothing
// says which application, and so which serving port, it should be checked on.
var ErrNoAppLabel = errors.New("infra carries no application label")

// WithHealthWait replaces how long a deployment waits for its application to
// answer, and how often it asks.
func WithHealthWait(timeout, interval time.Duration) Option {
	return func(s *Service) {
		if timeout > 0 {
			s.healthTimeout = timeout
		}
		if interval > 0 {
			s.healthInterval = interval
		}
	}
}

// healthCommand asks the application on its own node whether it answers.
//
// It goes to 127.0.0.1 through the CB-Tumblebug remote command channel rather
// than to the public address, so the answer says whether the server is up
// regardless of the firewall or of a node that has no public address. A node
// without curl says so instead of falling back to another client: an answer
// read through a different tool is a different measurement.
func healthCommand(app *model.AppSpec) string {
	path := app.Serving.HealthPath
	if path == "" {
		path = "/"
	}
	url := fmt.Sprintf("http://127.0.0.1:%d%s", app.Serving.Port, path)
	return fmt.Sprintf("if ! command -v curl >/dev/null 2>&1; then echo %s; exit 0; fi; "+
		"curl -s -o /dev/null -w '%%{http_code}' --max-time %d %s || true",
		healthNoCurl, healthCurlMaxTime, shellQuote(url))
}

// CheckServingHealth asks every node of a deployment once whether the
// application answers on its serving port.
//
// A node is ready on any 2xx or 3xx answer. Like the accelerator probe, this
// only reads, so the dry-run guard does not apply to it.
func (s *Service) CheckServingHealth(ctx context.Context, nsID, infraID string, app *model.AppSpec) (*model.ServingHealth, error) {
	req := &tumblebug.InfraCmdReq{
		Command:        []string{healthCommand(app)},
		UserName:       probeUserName(app),
		TimeoutMinutes: healthTimeoutMinutes,
	}
	results, err := s.tumblebug.RunCommand(ctx, nsID, infraID, req)
	if err != nil {
		return nil, err
	}
	if len(results.Results) == 0 {
		return nil, fmt.Errorf("no node of infra %q answered the health check", infraID)
	}

	health := &model.ServingHealth{
		InfraID:    infraID,
		AppID:      app.ID,
		Port:       app.Serving.Port,
		HealthPath: app.Serving.HealthPath,
		Attempts:   1,
		Nodes:      make([]model.NodeHealth, 0, len(results.Results)),
		CheckedAt:  time.Now().UTC(),
	}
	ready := 0
	for _, result := range results.Results {
		node := readNodeHealth(result, app.Serving.Port)
		if node.Ready {
			ready++
		}
		health.Nodes = append(health.Nodes, node)
	}
	health.Ready = ready == len(health.Nodes)
	health.Message = fmt.Sprintf("%d of %d nodes answer on port %d", ready, len(health.Nodes), app.Serving.Port)

	log.Info().Str("nsId", nsID).Str("infraId", infraID).Str("appId", app.ID).
		Bool("ready", health.Ready).Int("readyNodes", ready).Int("nodes", len(health.Nodes)).
		Msg("Checked serving health")

	return health, nil
}

// readNodeHealth turns one node's command result into its health entry.
func readNodeHealth(result tumblebug.CommandResult, port int) model.NodeHealth {
	node := model.NodeHealth{NodeID: result.NodeID, PublicIP: result.NodeIP}
	stdout := strings.TrimSpace(result.Stdout["0"])

	if strings.Contains(stdout, healthNoCurl) {
		node.Error = errNoCurl
		return node
	}
	code, err := strconv.Atoi(stdout)
	if err != nil {
		node.Error = result.Err
		if node.Error == "" {
			node.Error = fmt.Sprintf("unreadable health answer %q", stdout)
		}
		return node
	}
	node.HTTPCode = code
	switch {
	case code == 0:
		node.Error = fmt.Sprintf("nothing answered on 127.0.0.1:%d", port)
	case code >= 200 && code < 400:
		node.Ready = true
	}
	return node
}

// WaitServingHealthy checks the deployment until every node answers or the
// health timeout passes, and returns the last result either way.
//
// It never fails: the nodes are already up and billing, so an application that
// is still loading, or never comes up, is reported rather than turned into a
// failed deployment.
func (s *Service) WaitServingHealthy(ctx context.Context, nsID, infraID string, app *model.AppSpec) *model.ServingHealth {
	deadline := time.Now().Add(s.healthTimeout)
	var last *model.ServingHealth

	for attempt := 1; ; attempt++ {
		health, err := s.CheckServingHealth(ctx, nsID, infraID, app)
		if err != nil {
			log.Warn().Err(err).Str("nsId", nsID).Str("infraId", infraID).Int("attempt", attempt).
				Msg("Could not check serving health")
			health = uncheckedHealth(infraID, app)
		}
		health.Attempts = attempt
		last = health
		if health.Ready {
			return health
		}

		if time.Now().Add(s.healthInterval).After(deadline) {
			break
		}
		timer := time.NewTimer(s.healthInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			last.Message += fmt.Sprintf("; stopped waiting after %d attempts: %v", attempt, ctx.Err())
			return last
		case <-timer.C:
		}
	}

	last.Message += fmt.Sprintf("; not ready after %d attempts within %s", last.Attempts, s.healthTimeout)
	return last
}

// checkOnce is the single health check a deployment makes when it is not told
// to wait. A failed check is reported in the result, not as an error.
func (s *Service) checkOnce(ctx context.Context, nsID, infraID string, app *model.AppSpec) *model.ServingHealth {
	health, err := s.CheckServingHealth(ctx, nsID, infraID, app)
	if err != nil {
		log.Warn().Err(err).Str("nsId", nsID).Str("infraId", infraID).
			Msg("Could not check serving health")
		return uncheckedHealth(infraID, app)
	}
	return health
}

// uncheckedHealth is the result of a health check that got no answer at all.
func uncheckedHealth(infraID string, app *model.AppSpec) *model.ServingHealth {
	return &model.ServingHealth{
		InfraID:    infraID,
		AppID:      app.ID,
		Port:       app.Serving.Port,
		HealthPath: app.Serving.HealthPath,
		Attempts:   1,
		Nodes:      []model.NodeHealth{},
		CheckedAt:  time.Now().UTC(),
		Message:    "Serving health could not be checked on the deployed nodes",
	}
}

// DeployedApp reads which registered application an Infra was deployed with,
// from the label Plan writes.
func (s *Service) DeployedApp(ctx context.Context, nsID, infraID string) (*model.AppSpec, error) {
	infra, err := s.tumblebug.GetInfra(ctx, nsID, infraID)
	if err != nil {
		return nil, err
	}
	appID := infra.Label[labelAppID]
	if appID == "" {
		return nil, fmt.Errorf("%w: %s has no %s label", ErrNoAppLabel, infraID, labelAppID)
	}
	return s.catalog.Get(ctx, appID)
}
