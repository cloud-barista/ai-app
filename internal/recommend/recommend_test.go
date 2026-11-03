package recommend

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ish-hcc/ai-agent-proto/internal/model"
)

type nopModels struct{}

func (nopModels) Recommend(context.Context, *ModelRecommendReq) (*ModelRecommendResp, error) {
	return nil, nil
}

func TestOpenUnknownName(t *testing.T) {
	_, err := Open("missing", "", NewCatalog(nil, nil))
	if !errors.Is(err, ErrUnknownRecommender) || !strings.Contains(err.Error(), "AIAPP_RECOMMENDER_MODEL") {
		t.Errorf("Open model = %v, want ErrUnknownRecommender naming AIAPP_RECOMMENDER_MODEL", err)
	}
	_, err = Open("", "missing", NewCatalog(nil, nil))
	if !errors.Is(err, ErrUnknownRecommender) || !strings.Contains(err.Error(), "AIAPP_RECOMMENDER_APP") {
		t.Errorf("Open app = %v, want ErrUnknownRecommender naming AIAPP_RECOMMENDER_APP", err)
	}
}

func TestNotConfigured(t *testing.T) {
	s, err := Open("", "", NewCatalog(nil, nil))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := s.RecommendModels(context.Background(), &ModelRecommendReq{}); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("RecommendModels = %v, want ErrNotConfigured", err)
	}
	if _, err := s.RecommendApps(context.Background(), &AppRecommendReq{}); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("RecommendApps = %v, want ErrNotConfigured", err)
	}
	var none *Service
	if _, err := none.RecommendModels(context.Background(), &ModelRecommendReq{}); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("nil RecommendModels = %v, want ErrNotConfigured", err)
	}
}

func TestRegisterAndNilResponse(t *testing.T) {
	RegisterModel("nop", func(Catalog) (ModelRecommender, error) { return nopModels{}, nil })
	if names := ModelNames(); len(names) != 1 || names[0] != "nop" {
		t.Errorf("ModelNames = %v, want [nop]", names)
	}

	s, err := Open("nop", "", NewCatalog(nil, nil))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	resp, err := s.RecommendModels(context.Background(), &ModelRecommendReq{})
	if err != nil {
		t.Fatalf("RecommendModels: %v", err)
	}
	if resp.Recommender != "nop" || resp.Candidates == nil || len(resp.Candidates) != 0 {
		t.Errorf("resp = %+v, want nop with an empty, non-nil candidate list", resp)
	}

	for name, register := range map[string]func(){
		"twice":   func() { RegisterModel("nop", func(Catalog) (ModelRecommender, error) { return nopModels{}, nil }) },
		"invalid": func() { RegisterModel("No Name", func(Catalog) (ModelRecommender, error) { return nopModels{}, nil }) },
		"nil":     func() { RegisterApp("nil-factory", nil) },
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Errorf("register %s did not panic", name)
				}
			}()
			register()
		})
	}
}

func TestValidateRuntime(t *testing.T) {
	runtimes := []model.RuntimeProfile{{ID: model.RuntimeVLLM}}
	if err := (&ModelRecommendReq{RuntimeID: model.RuntimeVLLM}).Validate(runtimes); err != nil {
		t.Errorf("Validate vllm = %v", err)
	}
	err := (&ModelRecommendReq{RuntimeID: "nowhere"}).Validate(runtimes)
	if !errors.Is(err, model.ErrValidation) || !strings.Contains(model.ValidationMessage(err), "runtimeId") {
		t.Errorf("Validate nowhere = %v, want a validation error naming runtimeId", err)
	}
}
