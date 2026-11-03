// Package recommend is where model and application recommendation plugs into
// the AI-App service. It holds the request and response of
// POST /aiapp/recommendations/models and POST /aiapp/recommendations/apps, the
// ModelRecommender and AppRecommender interfaces, and the registry the service
// picks an implementation from. No implementation is built in: until one is
// registered and configured, both routes answer 501.
//
// To add an implementation, add a file to this package, for example
// internal/recommend/khu.go, that registers a factory from an init function:
//
//	func init() {
//		RegisterModel("khu", func(catalog Catalog) (ModelRecommender, error) {
//			return &khuModels{catalog: catalog}, nil
//		})
//	}
//
// RegisterApp does the same for applications. The factory runs once at startup
// and receives the Catalog, read access to the model catalog, the runtime
// profiles and the uploaded applications that candidates are chosen from. The
// service is pointed at an implementation by name with AIAPP_RECOMMENDER_MODEL
// and AIAPP_RECOMMENDER_APP; a name nothing registered stops it at startup. An
// implementation kept in its own package under internal/recommend registers the
// same way and is linked in with a blank import in cmd/ai-app-server/main.go.
//
// The service validates a request before the implementation sees it, and
// checks every candidate the implementation returns against the catalog: a
// candidate naming a model, artifact, runtime or application version that does
// not exist, or a runtime the artifact does not list, fails the request with
// 500 instead of reaching the caller. Tests register a fake implementation the
// same way; see internal/rest/recommendations_test.go.
package recommend

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/ish-hcc/ai-agent-proto/internal/appstore"
	"github.com/ish-hcc/ai-agent-proto/internal/model"
	"github.com/ish-hcc/ai-agent-proto/internal/modelcat"
)

// Bounds of a recommendation request.
const (
	// MaxCandidates bounds maxCandidates of a request.
	MaxCandidates = 50
	// MaxTargetNodes bounds targetNodes of a request, as many as one
	// deployment request takes.
	MaxTargetNodes = model.MaxNodeDeployTargets
	// MaxGPUCount bounds gpuCount of a request.
	MaxGPUCount = 64
)

// ErrNotConfigured is returned when no recommender is configured for the kind
// asked for.
var ErrNotConfigured = errors.New("no recommender is configured")

// ErrUnknownRecommender is returned when a configured name is not registered.
var ErrUnknownRecommender = errors.New("recommender not registered")

// ErrInvalidCandidate is returned when a recommender returns a candidate the
// catalog does not hold.
var ErrInvalidCandidate = errors.New("recommender returned an invalid candidate")

// ModelRecommender picks model artifacts and runtimes for a requirement.
//
// The request it receives is already validated. It returns candidates best
// first; the service cuts the list to maxCandidates and fills recommender.
type ModelRecommender interface {
	Recommend(ctx context.Context, req *ModelRecommendReq) (*ModelRecommendResp, error)
}

// AppRecommender picks uploaded application versions for a requirement.
//
// The request it receives is already validated. It returns candidates best
// first; the service cuts the list to maxCandidates and fills recommender.
type AppRecommender interface {
	Recommend(ctx context.Context, req *AppRecommendReq) (*AppRecommendResp, error)
}

// Catalog is the read access a recommender has to what it can recommend. Each
// call returns copies, so a recommender may keep or change them.
type Catalog interface {
	// Models reads every model of the model catalog with its artifacts,
	// ordered by id, as GET /aiapp/models returns them.
	Models(ctx context.Context) []model.Model
	// Runtimes reads every inference runtime profile, ordered by id, as
	// GET /aiapp/runtimes returns them.
	Runtimes() []model.RuntimeProfile
	// Apps reads every uploaded application with its versions, ordered by
	// name, as GET /aiapp/apps returns them.
	Apps(ctx context.Context) []model.App
}

// NewCatalog gives read access to the model catalog and the application store.
// A nil store reads as empty.
func NewCatalog(models *modelcat.Store, apps *appstore.Store) Catalog {
	return &storeCatalog{models: models, apps: apps}
}

type storeCatalog struct {
	models *modelcat.Store
	apps   *appstore.Store
}

func (c *storeCatalog) Models(ctx context.Context) []model.Model {
	if c.models == nil {
		return []model.Model{}
	}
	return c.models.List(ctx, modelcat.Filter{})
}

func (c *storeCatalog) Runtimes() []model.RuntimeProfile {
	return modelcat.Runtimes()
}

func (c *storeCatalog) Apps(ctx context.Context) []model.App {
	if c.apps == nil {
		return []model.App{}
	}
	return c.apps.List(ctx)
}

// TargetNode is a node the recommended model or application is meant to run
// on, named as CB-Tumblebug names it. The Catalog does not read nodes: a
// recommender that fits candidates to the node spec asks CB-Tumblebug itself.
type TargetNode struct {
	// NsID is the CB-Tumblebug namespace of the Infra.
	NsID string `json:"nsId" validate:"required" example:"etri"`
	// InfraID is the CB-Tumblebug Infra the node belongs to.
	InfraID string `json:"infraId,omitempty" example:"travel"`
	// NodeID is the node within the Infra. It needs infraId.
	NodeID string `json:"nodeId,omitempty" example:"vm1"`
}

// ResourceHints are what the caller knows of the GPUs and budget the candidates
// must fit. Every field is optional; absent means unknown, not zero.
type ResourceHints struct {
	// GPUCount is the number of GPUs available on one node. 0 means no GPU;
	// absent means unknown. At most 64.
	GPUCount *int `json:"gpuCount,omitempty" example:"1"`
	// GPUMemoryGiB is the memory of one GPU in GiB (2^30 bytes).
	GPUMemoryGiB *float64 `json:"gpuMemoryGiB,omitempty" example:"24"`
	// MaxSizeGiB is the largest download, in GiB, the caller accepts for one
	// model artifact or application.
	MaxSizeGiB *float64 `json:"maxSizeGiB,omitempty" example:"20"`
}

// ModelRecommendReq says what the caller needs a model for.
//
// Every field is optional; an empty request asks for any model. Fields a
// recommender does not use are ignored by it, not refused.
type ModelRecommendReq struct {
	// Tasks are what the model is to be used for, in the words of the catalog
	// tasks[]: chat, vision-chat or embedding.
	Tasks []string `json:"tasks,omitempty" example:"vision-chat"`
	// InputModalities are the kinds of input the model must accept, in the
	// words of the catalog inputModalities[]: text or image.
	InputModalities []string `json:"inputModalities,omitempty" example:"text,image"`
	// RuntimeID is the runtime profile the caller prefers, as listed by
	// GET /runtimes. It must name an existing profile.
	RuntimeID string `json:"runtimeId,omitempty" example:"vllm"`
	ResourceHints
	// Requirements carry anything else the recommender reads, such as a cost
	// budget, a latency target or a language. The keys are the recommender's
	// own; the service passes them through unread.
	Requirements map[string]any `json:"requirements,omitempty"`
	// TargetNodes are the nodes the model is meant to run on, at most 32.
	TargetNodes []TargetNode `json:"targetNodes,omitempty"`
	// MaxCandidates bounds the candidates returned, at most 50. 0 leaves it to
	// the recommender.
	MaxCandidates int `json:"maxCandidates,omitempty" example:"3"`
}

// ModelCandidate is one recommended pairing of a model artifact and a runtime.
// modelId, artifactId and runtimeId go unchanged into the inference field of
// POST /deployments.
type ModelCandidate struct {
	// ModelID is a model of the model catalog (GET /models).
	ModelID string `json:"modelId" validate:"required" example:"qwen3-vl-8b"`
	// ArtifactID is an artifact of that model.
	ArtifactID string `json:"artifactId" validate:"required" example:"hf-bf16"`
	// RuntimeID is a runtime profile the artifact lists.
	RuntimeID string `json:"runtimeId" validate:"required" example:"vllm"`
	// Score ranks the candidate: higher is better. The scale is the
	// recommender's own.
	Score float64 `json:"score" example:"0.82"`
	// Reason says, for a person, why the candidate was picked.
	Reason string `json:"reason" example:"Accepts text and image input and fits one 24 GiB GPU in BF16"`
}

// ModelRecommendResp is the model recommendation body.
type ModelRecommendResp struct {
	// Recommender is the name of the implementation that answered, as set in
	// AIAPP_RECOMMENDER_MODEL.
	Recommender string `json:"recommender" example:"khu"`
	// Candidates are ordered best first. Empty means nothing in the catalog
	// fits.
	Candidates []ModelCandidate `json:"candidates"`
}

// InferenceRef names an inference service the application is to use: a model
// artifact served with a runtime, as in the inference field of
// POST /deployments.
type InferenceRef struct {
	// ModelID is a model of the model catalog (GET /models).
	ModelID string `json:"modelId" validate:"required" example:"qwen3-vl-8b"`
	// ArtifactID is an artifact of that model. Empty means any.
	ArtifactID string `json:"artifactId,omitempty" example:"hf-bf16"`
	// RuntimeID is the runtime profile that serves it. Empty means any.
	RuntimeID string `json:"runtimeId,omitempty" example:"vllm"`
}

// AppRecommendReq says what the caller needs an application for.
//
// Every field is optional; an empty request asks for any application. Fields a
// recommender does not use are ignored by it, not refused.
type AppRecommendReq struct {
	// Tasks are what the application is to be used for, such as chat-ui, rag
	// or travel-planner. Uploaded applications carry no tags, so the words are
	// the recommender's own.
	Tasks []string `json:"tasks,omitempty" example:"chat-ui"`
	// Inference are the inference services the application is to use, each
	// naming a model of the catalog.
	Inference []InferenceRef `json:"inference,omitempty"`
	ResourceHints
	// Requirements carry anything else the recommender reads. The keys are the
	// recommender's own; the service passes them through unread.
	Requirements map[string]any `json:"requirements,omitempty"`
	// TargetNodes are the nodes the application is meant to run on, at most 32.
	TargetNodes []TargetNode `json:"targetNodes,omitempty"`
	// MaxCandidates bounds the candidates returned, at most 50. 0 leaves it to
	// the recommender.
	MaxCandidates int `json:"maxCandidates,omitempty" example:"3"`
}

// AppCandidate is one recommended application version. name and version go
// unchanged into the app field of POST /deployments.
type AppCandidate struct {
	// Name is an uploaded application (GET /apps).
	Name string `json:"name" validate:"required" example:"open-webui"`
	// Version is a version of that application.
	Version int `json:"version" validate:"required" example:"1"`
	// Score ranks the candidate: higher is better. The scale is the
	// recommender's own.
	Score float64 `json:"score" example:"0.74"`
	// Reason says, for a person, why the candidate was picked.
	Reason string `json:"reason" example:"Chat interface that talks to an OpenAI compatible API"`
}

// AppRecommendResp is the application recommendation body.
type AppRecommendResp struct {
	// Recommender is the name of the implementation that answered, as set in
	// AIAPP_RECOMMENDER_APP.
	Recommender string `json:"recommender" example:"khu"`
	// Candidates are ordered best first. Empty means no uploaded application
	// fits.
	Candidates []AppCandidate `json:"candidates"`
}

// Validate reports why a model recommendation request cannot be answered.
// runtimes are the profiles runtimeId must be one of.
func (r *ModelRecommendReq) Validate(runtimes []model.RuntimeProfile) error {
	if err := validateWords("tasks", r.Tasks); err != nil {
		return err
	}
	if err := validateWords("inputModalities", r.InputModalities); err != nil {
		return err
	}
	if r.RuntimeID != "" && !hasRuntime(runtimes, r.RuntimeID) {
		return invalid("runtimeId", fmt.Sprintf("%q is not a runtime profile (GET /runtimes)", r.RuntimeID))
	}
	if err := r.ResourceHints.validate(); err != nil {
		return err
	}
	if err := validateTargets(r.TargetNodes); err != nil {
		return err
	}
	return validateMaxCandidates(r.MaxCandidates)
}

// Validate reports why an application recommendation request cannot be
// answered. models are the catalog models inference[] must name.
func (r *AppRecommendReq) Validate(models []model.Model) error {
	if err := validateWords("tasks", r.Tasks); err != nil {
		return err
	}
	for i, ref := range r.Inference {
		field := fmt.Sprintf("inference[%d]", i)
		if ref.ModelID == "" {
			return invalid(field+".modelId", "required")
		}
		m := findModel(models, ref.ModelID)
		if m == nil {
			return invalid(field+".modelId", fmt.Sprintf("%q is not a model of the catalog (GET /models)", ref.ModelID))
		}
		if ref.ArtifactID != "" {
			if _, ok := m.Artifact(ref.ArtifactID); !ok {
				return invalid(field+".artifactId", fmt.Sprintf("%q is not an artifact of %s", ref.ArtifactID, ref.ModelID))
			}
		}
	}
	if err := r.ResourceHints.validate(); err != nil {
		return err
	}
	if err := validateTargets(r.TargetNodes); err != nil {
		return err
	}
	return validateMaxCandidates(r.MaxCandidates)
}

func (h ResourceHints) validate() error {
	if h.GPUCount != nil && (*h.GPUCount < 0 || *h.GPUCount > MaxGPUCount) {
		return invalid("gpuCount", fmt.Sprintf("must be between 0 and %d", MaxGPUCount))
	}
	if h.GPUMemoryGiB != nil && *h.GPUMemoryGiB < 0 {
		return invalid("gpuMemoryGiB", "must not be negative")
	}
	if h.MaxSizeGiB != nil && *h.MaxSizeGiB < 0 {
		return invalid("maxSizeGiB", "must not be negative")
	}
	return nil
}

func validateWords(field string, words []string) error {
	for i, word := range words {
		if strings.TrimSpace(word) == "" {
			return invalid(fmt.Sprintf("%s[%d]", field, i), "required")
		}
	}
	return nil
}

func validateTargets(targets []TargetNode) error {
	if len(targets) > MaxTargetNodes {
		return invalid("targetNodes", fmt.Sprintf("must hold at most %d nodes", MaxTargetNodes))
	}
	for i, target := range targets {
		field := fmt.Sprintf("targetNodes[%d]", i)
		if target.NsID == "" {
			return invalid(field+".nsId", "required")
		}
		if target.NodeID != "" && target.InfraID == "" {
			return invalid(field+".infraId", "required when nodeId is set")
		}
	}
	return nil
}

func validateMaxCandidates(n int) error {
	if n < 0 || n > MaxCandidates {
		return invalid("maxCandidates", fmt.Sprintf("must be between 0 and %d", MaxCandidates))
	}
	return nil
}

func invalid(field, reason string) error {
	return &model.ValidationError{Message: field + " " + reason}
}

func hasRuntime(runtimes []model.RuntimeProfile, runtimeID string) bool {
	for _, runtime := range runtimes {
		if runtime.ID == runtimeID {
			return true
		}
	}
	return false
}

func findModel(models []model.Model, modelID string) *model.Model {
	for i := range models {
		if models[i].ID == modelID {
			return &models[i]
		}
	}
	return nil
}
