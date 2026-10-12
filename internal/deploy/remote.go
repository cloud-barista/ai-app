package deploy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/rs/zerolog/log"

	"github.com/ish-hcc/ai-agent-proto/internal/config"
	"github.com/ish-hcc/ai-agent-proto/internal/model"
)

// remoteSourceName marks plan values decided by a remote backend.
const remoteSourceName = "remote"

// maxRemoteErrorBody bounds how much of a failed response ends up in an error.
const maxRemoteErrorBody = 512

// RemoteError reports a non-2xx answer from a remote backend.
type RemoteError struct {
	Method     string
	URL        string
	StatusCode int
	// Body is the start of the response body, for whoever reads the log.
	Body string
}

func (e *RemoteError) Error() string {
	return fmt.Sprintf("%s %s answered %d: %s", e.Method, e.URL, e.StatusCode, e.Body)
}

// remoteClient posts JSON to an external placement service.
type remoteClient struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

func newRemoteClient(cfg config.BackendConfig) *remoteClient {
	return &remoteClient{
		baseURL:    strings.TrimRight(cfg.URL, "/"),
		token:      cfg.Token,
		httpClient: &http.Client{Timeout: cfg.Timeout},
	}
}

// post sends body as JSON to path and decodes the JSON answer into out.
func (c *remoteClient) post(ctx context.Context, path string, body, out any) error {
	endpoint := c.baseURL + path
	encoded, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("failed to encode request body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return fmt.Errorf("failed to build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	log.Debug().Str("method", http.MethodPost).Str("url", endpoint).Msg("Calling remote placement service")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to call %s: %w", endpoint, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxRemoteErrorBody))
		return &RemoteError{
			Method:     http.MethodPost,
			URL:        endpoint,
			StatusCode: resp.StatusCode,
			Body:       strings.TrimSpace(string(raw)),
		}
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("failed to decode response of %s: %w", endpoint, err)
	}
	return nil
}

// remoteRecommender asks an external service where an application goes.
type remoteRecommender struct {
	client *remoteClient
}

// NewRemoteRecommender builds a Recommender that calls cfg.URL.
func NewRemoteRecommender(cfg config.BackendConfig) Recommender {
	return &remoteRecommender{client: newRemoteClient(cfg)}
}

// SourceName marks plan values this recommender decided.
func (r *remoteRecommender) SourceName() string { return remoteSourceName }

// RecommendSpecs asks the remote service for node specs.
func (r *remoteRecommender) RecommendSpecs(ctx context.Context, app *model.AppSpec, limit int) ([]model.SpecCandidate, error) {
	if limit <= 0 {
		limit = defaultSpecCandidates
	}

	req := struct {
		App   *model.AppSpec `json:"app"`
		Limit int            `json:"limit"`
	}{App: app, Limit: limit}
	var resp struct {
		Candidates []model.SpecCandidate `json:"candidates"`
		Reason     string                `json:"reason"`
	}
	if err := r.client.post(ctx, "/recommendations/specs", req, &resp); err != nil {
		return nil, fmt.Errorf("failed to recommend specs for %q: %w", app.ID, err)
	}

	log.Info().Str("appId", app.ID).Int("candidates", len(resp.Candidates)).
		Str("reason", resp.Reason).Msg("Remote recommender proposed specs")

	if resp.Candidates == nil {
		return []model.SpecCandidate{}, nil
	}
	return resp.Candidates, nil
}

// ResolveImage asks the remote service for the node image of a spec.
func (r *remoteRecommender) ResolveImage(ctx context.Context, app *model.AppSpec, specID string) (*ImageChoice, error) {
	req := struct {
		App    *model.AppSpec `json:"app"`
		SpecID string         `json:"specId"`
	}{App: app, SpecID: specID}
	var resp struct {
		ImageID string `json:"imageId"`
		Source  string `json:"source"`
		Reason  string `json:"reason"`
	}
	if err := r.client.post(ctx, "/recommendations/image", req, &resp); err != nil {
		return nil, fmt.Errorf("failed to resolve image for %q on %q: %w", app.ID, specID, err)
	}
	if resp.ImageID == "" {
		return nil, fmt.Errorf("remote recommender named no image for %q on %q", app.ID, specID)
	}

	log.Info().Str("appId", app.ID).Str("specId", specID).Str("imageId", resp.ImageID).
		Str("source", resp.Source).Msg("Remote recommender resolved node image")

	return &ImageChoice{
		ID:     resp.ImageID,
		Source: resp.Source,
		Reason: resp.Reason,
	}, nil
}

// remoteProvisioner asks an external service to review, create and update Infras.
type remoteProvisioner struct {
	client *remoteClient
}

// NewRemoteProvisioner builds a Provisioner that calls cfg.URL.
func NewRemoteProvisioner(cfg config.BackendConfig) Provisioner {
	return &remoteProvisioner{client: newRemoteClient(cfg)}
}

// provisionReq is the body of a remote review or create.
type provisionReq struct {
	NsID string                `json:"nsId"`
	Plan *model.DeploymentPlan `json:"plan"`
}

// Review asks the remote service to validate a plan without creating anything.
func (p *remoteProvisioner) Review(ctx context.Context, plan *model.DeploymentPlan) (*model.ReviewSummary, error) {
	review := &model.ReviewSummary{}
	req := provisionReq{NsID: plan.Namespace, Plan: plan}
	if err := p.client.post(ctx, "/provisions/review", req, review); err != nil {
		return nil, fmt.Errorf("failed to review %q: %w", plan.InfraName, err)
	}
	return review, nil
}

// Create asks the remote service to provision a plan.
func (p *remoteProvisioner) Create(ctx context.Context, nsID string, plan *model.DeploymentPlan) (*model.ProvisionResult, error) {
	result := &model.ProvisionResult{}
	if err := p.client.post(ctx, "/provisions", provisionReq{NsID: nsID, Plan: plan}, result); err != nil {
		return nil, fmt.Errorf("failed to provision %q: %w", plan.InfraName, err)
	}
	return result, nil
}

// Update asks the remote service to apply a new application version to an Infra.
func (p *remoteProvisioner) Update(ctx context.Context, nsID, infraID string, app *model.AppSpec) (*model.ProvisionResult, error) {
	req := struct {
		NsID string         `json:"nsId"`
		App  *model.AppSpec `json:"app"`
	}{NsID: nsID, App: app}
	result := &model.ProvisionResult{}
	path := "/provisions/" + url.PathEscape(infraID) + "/update"
	if err := p.client.post(ctx, path, req, result); err != nil {
		return nil, fmt.Errorf("failed to update %q: %w", infraID, err)
	}
	return result, nil
}

// OptionsFromConfig turns the recommender and provisioner settings into
// Service options. Local settings add nothing, so the defaults stay in place.
func OptionsFromConfig(recommender, provisioner config.BackendConfig) []Option {
	var opts []Option
	if recommender.Kind == config.BackendRemote {
		opts = append(opts, WithRecommender(NewRemoteRecommender(recommender)))
	}
	if provisioner.Kind == config.BackendRemote {
		opts = append(opts, WithProvisioner(NewRemoteProvisioner(provisioner)))
	}
	return opts
}
