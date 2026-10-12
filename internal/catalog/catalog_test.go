package catalog

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ish-hcc/ai-agent-proto/internal/model"
)

func application(requires ...model.AppRequirement) model.AppSpec {
	return model.AppSpec{
		ID: "chat-ui", Name: "Chat UI", Version: "0.1.0", Runtime: model.RuntimeOther,
		Kind:     model.KindApplication,
		Requires: requires,
		Serving:  model.ServingSpec{Port: 8080},
		Install:  model.InstallSpec{Commands: []string{"echo install"}},
	}
}

func TestSeedRegistersModelServicesAndOneApplication(t *testing.T) {
	store := seededStore(t)
	apps := store.List(context.Background())
	if len(apps) != 4 {
		t.Fatalf("seeded %d applications, want 4", len(apps))
	}
	kinds := map[string]model.AppKind{}
	for _, app := range apps {
		kinds[app.ID] = app.Kind
	}
	for _, id := range []string{"vllm-llama31-8b", "ollama-qwen25-7b", "triton-resnet50"} {
		if kinds[id] != model.KindModelService {
			t.Errorf("%s kind = %q, want model-service", id, kinds[id])
		}
	}
	if kinds["open-webui-llama31"] != model.KindApplication {
		t.Errorf("open-webui-llama31 kind = %q, want application", kinds["open-webui-llama31"])
	}
}

func TestRegisterChecksRequires(t *testing.T) {
	cases := []struct {
		name    string
		appID   string
		wantErr string
	}{
		{"model service", "vllm-llama31-8b", ""},
		{"not registered", "vllm-missing", `"vllm-missing" is not a registered application`},
		{"application", "open-webui-llama31", `"open-webui-llama31" is not a model service`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := seededStore(t)
			_, err := store.Register(context.Background(),
				application(model.AppRequirement{AppID: tc.appID, Env: "OPENAI_API_BASE_URL"}))
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Register() = %v, want nil", err)
				}
				return
			}
			if err == nil || !errors.Is(err, model.ErrValidation) || !strings.Contains(model.ValidationMessage(err), tc.wantErr) {
				t.Fatalf("Register() = %v, want validation error containing %q", err, tc.wantErr)
			}
			if _, getErr := store.Get(context.Background(), "chat-ui"); getErr == nil {
				t.Error("refused application was stored anyway")
			}
		})
	}
}

func TestRegisterDefaultsKindToModelService(t *testing.T) {
	store := NewStore()
	spec := model.AppSpec{
		ID: "m", Name: "m", Version: "0.1.0", Runtime: model.RuntimeVLLM,
		Model:       model.ModelRef{ID: "m"},
		Accelerator: model.AcceleratorRequirement{Type: "gpu", MinCount: 1},
		Serving:     model.ServingSpec{Port: 8000},
		Install:     model.InstallSpec{Commands: []string{"echo"}},
	}
	registered, err := store.Register(context.Background(), spec)
	if err != nil {
		t.Fatalf("Register() = %v", err)
	}
	if registered.Kind != model.KindModelService {
		t.Errorf("kind = %q, want model-service", registered.Kind)
	}
}

func TestResolveFindsChatApplication(t *testing.T) {
	store := seededStore(t)
	matches := store.Resolve(context.Background(), "챗봇 하나 띄워줘", 3)
	if len(matches) == 0 || matches[0].AppID != "open-webui-llama31" {
		t.Fatalf("Resolve(챗봇) = %+v, want open-webui-llama31 first", matches)
	}
	if matches[0].Kind != model.KindApplication || len(matches[0].Requires) != 1 ||
		matches[0].Requires[0].AppID != "vllm-llama31-8b" {
		t.Errorf("match = %+v, want kind application requiring vllm-llama31-8b", matches[0])
	}
}
