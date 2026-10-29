package model

import (
	"errors"
	"strings"
	"testing"
)

func validModel() Model {
	return Model{
		ID: "qwen3-vl-8b", Name: "Qwen3-VL-8B-Instruct",
		Tasks: []string{"vision-chat"}, InputModalities: []string{"text", "image"},
		Artifacts: []Artifact{
			{ID: "hf-bf16", Source: "huggingface", Ref: "Qwen/Qwen3-VL-8B-Instruct", Format: FormatHF,
				Engines: []EngineCompat{{Engine: EngineVLLM}}},
			{ID: "ollama-gguf", Source: "ollama", Ref: "qwen3-vl:8b", Format: FormatGGUF,
				Engines: []EngineCompat{{Engine: EngineOllama}}},
		},
	}
}

func TestModelValidate(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*Model)
		wantErr string
	}{
		{"valid", func(m *Model) {}, ""},
		{"no id", func(m *Model) { m.ID = "" }, "id required"},
		{"upper case id", func(m *Model) { m.ID = "Qwen3" }, "id must match"},
		{"id with slash", func(m *Model) { m.ID = "a/b" }, "id must match"},
		{"no name", func(m *Model) { m.Name = "" }, "name required"},
		{"negative context", func(m *Model) { m.ContextLength = -1 }, "contextLength must not be negative"},
		{"no artifacts", func(m *Model) { m.Artifacts = nil }, "artifacts must hold"},
		{"empty task", func(m *Model) { m.Tasks = []string{""} }, "tasks[0] required"},
		{"empty modality", func(m *Model) { m.InputModalities = []string{"text", ""} }, "inputModalities[1] required"},
		{"artifact without id", func(m *Model) { m.Artifacts[1].ID = "" }, "artifacts[1].id required"},
		{"duplicate artifact id", func(m *Model) { m.Artifacts[1].ID = "hf-bf16" }, "artifacts[1].id \"hf-bf16\" is used"},
		{"artifact without source", func(m *Model) { m.Artifacts[0].Source = "" }, "artifacts[0].source required"},
		{"artifact without ref", func(m *Model) { m.Artifacts[0].Ref = "" }, "artifacts[0].ref required"},
		{"ref with space", func(m *Model) { m.Artifacts[0].Ref = "a b" }, "artifacts[0].ref must match"},
		{"ref with quote", func(m *Model) { m.Artifacts[0].Ref = `a"b` }, "artifacts[0].ref must match"},
		{"ref with dollar", func(m *Model) { m.Artifacts[0].Ref = "a$b" }, "artifacts[0].ref must match"},
		{"artifact without format", func(m *Model) { m.Artifacts[0].Format = "" }, "artifacts[0].format required"},
		{"negative size", func(m *Model) { m.Artifacts[0].SizeGiB = -1 }, "artifacts[0].sizeGiB must not be negative"},
		{"artifact without engines", func(m *Model) { m.Artifacts[0].Engines = nil }, "artifacts[0].engines must name"},
		{"engine without name", func(m *Model) { m.Artifacts[0].Engines = []EngineCompat{{}} }, "artifacts[0].engines[0].engine required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := validModel()
			tc.mutate(&m)
			err := m.Validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate: %v", err)
				}
				return
			}
			if !errors.Is(err, ErrValidation) || !strings.Contains(ValidationMessage(err), tc.wantErr) {
				t.Errorf("Validate error = %v, want a validation error containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestEngineProfileValidate(t *testing.T) {
	valid := func() EngineProfile {
		return EngineProfile{ID: "vllm", Image: "vllm/vllm-openai:v0.32.0", Formats: []string{FormatHF},
			Port: 8000, HealthPath: "/health", APIBasePath: "/v1"}
	}
	cases := []struct {
		name    string
		mutate  func(*EngineProfile)
		wantErr string
	}{
		{"valid", func(e *EngineProfile) {}, ""},
		{"no image", func(e *EngineProfile) { e.Image = "" }, "image required"},
		{"no formats", func(e *EngineProfile) { e.Formats = nil }, "formats must name"},
		{"bad port", func(e *EngineProfile) { e.Port = 0 }, "port must be between"},
		{"relative health path", func(e *EngineProfile) { e.HealthPath = "health" }, "healthPath must start with /"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := valid()
			tc.mutate(&e)
			err := e.Validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate: %v", err)
				}
				return
			}
			if !strings.Contains(ValidationMessage(err), tc.wantErr) {
				t.Errorf("Validate error = %v, want one containing %q", err, tc.wantErr)
			}
		})
	}
}
