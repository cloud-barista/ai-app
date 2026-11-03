package recommend

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/ish-hcc/ai-agent-proto/internal/model"
)

// namePattern is what a recommender name may look like. It is set in an
// environment variable and returned in every response.
var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)

// ModelFactory builds a model recommender at startup from the catalog it reads
// candidates from.
type ModelFactory func(catalog Catalog) (ModelRecommender, error)

// AppFactory builds an application recommender at startup from the catalog it
// reads candidates from.
type AppFactory func(catalog Catalog) (AppRecommender, error)

// The settings that name the configured recommenders, quoted in errors.
const (
	envModelName = "AIAPP_RECOMMENDER_MODEL"
	envAppName   = "AIAPP_RECOMMENDER_APP"
)

var (
	registryMu   sync.RWMutex
	modelFactory = map[string]ModelFactory{}
	appFactory   = map[string]AppFactory{}
)

// ValidName reports whether name may name a recommender: lower case letters,
// digits, ".", "_" and "-", at most 63 characters.
func ValidName(name string) bool {
	return namePattern.MatchString(name)
}

// RegisterModel makes a model recommender available under name. It is meant
// to be called from an init function and panics when name is invalid or
// already taken, as two implementations under one name are a build mistake.
func RegisterModel(name string, factory ModelFactory) {
	registryMu.Lock()
	defer registryMu.Unlock()
	mustRegister(name, factory == nil, modelFactory[name] != nil)
	modelFactory[name] = factory
}

// RegisterApp makes an application recommender available under name, as
// RegisterModel does for models.
func RegisterApp(name string, factory AppFactory) {
	registryMu.Lock()
	defer registryMu.Unlock()
	mustRegister(name, factory == nil, appFactory[name] != nil)
	appFactory[name] = factory
}

func mustRegister(name string, nilFactory, taken bool) {
	switch {
	case !ValidName(name):
		panic(fmt.Sprintf("recommend: invalid recommender name %q", name))
	case nilFactory:
		panic(fmt.Sprintf("recommend: nil factory for recommender %q", name))
	case taken:
		panic(fmt.Sprintf("recommend: recommender %q registered twice", name))
	}
}

// ModelNames lists the registered model recommenders, sorted.
func ModelNames() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	return sortedKeys(modelFactory)
}

// AppNames lists the registered application recommenders, sorted.
func AppNames() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	return sortedKeys(appFactory)
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for name := range m {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Service answers recommendation requests with the configured recommenders.
// A request is validated first; a valid one of a kind with no recommender
// configured answers ErrNotConfigured. A nil Service answers ErrNotConfigured
// to everything.
type Service struct {
	catalog   Catalog
	modelName string
	model     ModelRecommender
	appName   string
	app       AppRecommender
}

// Open builds the recommenders named by modelName and appName. An empty name
// leaves that kind unconfigured. A name nothing registered fails with
// ErrUnknownRecommender.
func Open(modelName, appName string, catalog Catalog) (*Service, error) {
	s := &Service{catalog: catalog, modelName: modelName, appName: appName}
	if modelName != "" {
		registryMu.RLock()
		factory := modelFactory[modelName]
		registryMu.RUnlock()
		if factory == nil {
			return nil, unknown(envModelName, modelName, ModelNames())
		}
		recommender, err := factory(catalog)
		if err != nil {
			return nil, fmt.Errorf("failed to build model recommender %q: %w", modelName, err)
		}
		s.model = recommender
	}
	if appName != "" {
		registryMu.RLock()
		factory := appFactory[appName]
		registryMu.RUnlock()
		if factory == nil {
			return nil, unknown(envAppName, appName, AppNames())
		}
		recommender, err := factory(catalog)
		if err != nil {
			return nil, fmt.Errorf("failed to build application recommender %q: %w", appName, err)
		}
		s.app = recommender
	}
	return s, nil
}

func unknown(env, name string, registered []string) error {
	return fmt.Errorf("%w: %s=%q, registered: %s", ErrUnknownRecommender, env, name, describe(registered))
}

func describe(names []string) string {
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, ", ")
}

// ModelName is the configured model recommender, empty when none is.
func (s *Service) ModelName() string {
	if s == nil {
		return ""
	}
	return s.modelName
}

// AppName is the configured application recommender, empty when none is.
func (s *Service) AppName() string {
	if s == nil {
		return ""
	}
	return s.appName
}

func modelNotConfigured() error {
	return fmt.Errorf("%w for models: set %s to a registered model recommender (registered: %s)",
		ErrNotConfigured, envModelName, describe(ModelNames()))
}

func appNotConfigured() error {
	return fmt.Errorf("%w for applications: set %s to a registered application recommender (registered: %s)",
		ErrNotConfigured, envAppName, describe(AppNames()))
}

// RecommendModels validates req, asks the model recommender and checks every
// candidate against the catalog.
func (s *Service) RecommendModels(ctx context.Context, req *ModelRecommendReq) (*ModelRecommendResp, error) {
	if s == nil {
		return nil, modelNotConfigured()
	}
	runtimes := s.catalog.Runtimes()
	if err := req.Validate(runtimes); err != nil {
		return nil, err
	}
	if s.model == nil {
		return nil, modelNotConfigured()
	}

	resp, err := s.model.Recommend(ctx, req)
	if err != nil {
		return nil, err
	}
	out := &ModelRecommendResp{Recommender: s.modelName, Candidates: []ModelCandidate{}}
	if resp != nil && resp.Candidates != nil {
		out.Candidates = resp.Candidates
	}
	models := s.catalog.Models(ctx)
	for i, candidate := range out.Candidates {
		if err := checkModelCandidate(models, runtimes, candidate); err != nil {
			return nil, fmt.Errorf("%w: candidates[%d]: %s", ErrInvalidCandidate, i, err)
		}
	}
	if req.MaxCandidates > 0 && len(out.Candidates) > req.MaxCandidates {
		out.Candidates = out.Candidates[:req.MaxCandidates]
	}
	return out, nil
}

// RecommendApps validates req, asks the application recommender and checks
// every candidate against the application store.
func (s *Service) RecommendApps(ctx context.Context, req *AppRecommendReq) (*AppRecommendResp, error) {
	if s == nil {
		return nil, appNotConfigured()
	}
	if err := req.Validate(s.catalog.Models(ctx)); err != nil {
		return nil, err
	}
	if s.app == nil {
		return nil, appNotConfigured()
	}

	resp, err := s.app.Recommend(ctx, req)
	if err != nil {
		return nil, err
	}
	out := &AppRecommendResp{Recommender: s.appName, Candidates: []AppCandidate{}}
	if resp != nil && resp.Candidates != nil {
		out.Candidates = resp.Candidates
	}
	apps := s.catalog.Apps(ctx)
	for i, candidate := range out.Candidates {
		if err := checkAppCandidate(apps, candidate); err != nil {
			return nil, fmt.Errorf("%w: candidates[%d]: %s", ErrInvalidCandidate, i, err)
		}
	}
	if req.MaxCandidates > 0 && len(out.Candidates) > req.MaxCandidates {
		out.Candidates = out.Candidates[:req.MaxCandidates]
	}
	return out, nil
}

func checkModelCandidate(models []model.Model, runtimes []model.RuntimeProfile, c ModelCandidate) error {
	m := findModel(models, c.ModelID)
	if m == nil {
		return fmt.Errorf("model %q is not in the catalog", c.ModelID)
	}
	artifact, ok := m.Artifact(c.ArtifactID)
	if !ok {
		return fmt.Errorf("artifact %q is not an artifact of %s", c.ArtifactID, c.ModelID)
	}
	if !hasRuntime(runtimes, c.RuntimeID) {
		return fmt.Errorf("runtime %q is not a runtime profile", c.RuntimeID)
	}
	if !artifact.SupportsRuntime(c.RuntimeID) {
		return fmt.Errorf("artifact %s/%s does not list runtime %q", c.ModelID, c.ArtifactID, c.RuntimeID)
	}
	return nil
}

func checkAppCandidate(apps []model.App, c AppCandidate) error {
	for i := range apps {
		if apps[i].Name != c.Name {
			continue
		}
		if _, ok := apps[i].Version(c.Version); !ok {
			return fmt.Errorf("application %s has no version %d", c.Name, c.Version)
		}
		return nil
	}
	return fmt.Errorf("application %q is not uploaded", c.Name)
}
