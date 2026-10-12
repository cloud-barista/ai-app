package model

import (
	"errors"
	"strings"
	"testing"
)

func validModelService() AppSpec {
	return AppSpec{
		ID: "vllm-llama31-8b", Name: "vLLM", Version: "0.1.0", Runtime: RuntimeVLLM,
		Model:       ModelRef{ID: "meta-llama/Llama-3.1-8B-Instruct"},
		Accelerator: AcceleratorRequirement{Type: "gpu", MinCount: 1},
		Serving:     ServingSpec{Port: 8000},
		Install:     InstallSpec{Commands: []string{"echo install"}},
	}
}

func validApplication() AppSpec {
	return AppSpec{
		ID: "open-webui-llama31", Name: "Open WebUI", Version: "0.1.0", Runtime: RuntimeOther,
		Kind:     KindApplication,
		Requires: []AppRequirement{{AppID: "vllm-llama31-8b", Env: "OPENAI_API_BASE_URL"}},
		Serving:  ServingSpec{Port: 8080},
		Install:  InstallSpec{Commands: []string{"echo install"}},
	}
}

func TestValidateKinds(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*AppSpec)
		base    func() AppSpec
		wantErr string
	}{
		{"model service without kind", func(a *AppSpec) {}, validModelService, ""},
		{"model service with kind", func(a *AppSpec) { a.Kind = KindModelService }, validModelService, ""},
		{"unknown kind", func(a *AppSpec) { a.Kind = "service" }, validModelService, "kind must be"},
		{"model service with requires", func(a *AppSpec) {
			a.Requires = []AppRequirement{{AppID: "x", Env: "X"}}
		}, validModelService, "requires is only allowed"},
		{"model service without model", func(a *AppSpec) { a.Model = ModelRef{} }, validModelService, "model.id required"},
		{"application without model or accelerator", func(a *AppSpec) {}, validApplication, ""},
		{"application without requires", func(a *AppSpec) { a.Requires = nil }, validApplication, "requires must name"},
		{"application requirement without appId", func(a *AppSpec) { a.Requires[0].AppID = "" }, validApplication, "requires[0].appId required"},
		{"application requirement without env", func(a *AppSpec) { a.Requires[0].Env = "" }, validApplication, "requires[0].env required"},
		{"lowercase env", func(a *AppSpec) { a.Requires[0].Env = "openai_api_base_url" }, validApplication, "requires[0].env must match"},
		{"env starting with digit", func(a *AppSpec) { a.Requires[0].Env = "1URL" }, validApplication, "requires[0].env must match"},
		{"env with dash", func(a *AppSpec) { a.Requires[0].Env = "API-URL" }, validApplication, "requires[0].env must match"},
		{"application accelerator without count", func(a *AppSpec) {
			a.Accelerator = AcceleratorRequirement{Type: "gpu"}
		}, validApplication, "accelerator.minCount"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := tc.base()
			tc.mutate(&spec)
			err := spec.Validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil || !errors.Is(err, ErrValidation) || !strings.Contains(ValidationMessage(err), tc.wantErr) {
				t.Fatalf("Validate() = %v, want validation error containing %q", err, tc.wantErr)
			}
		})
	}
}
