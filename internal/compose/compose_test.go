package compose

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/ish-hcc/ai-agent-proto/internal/model"
	"github.com/ish-hcc/ai-agent-proto/internal/modelcat"
)

func artifactOf(t *testing.T, modelID, artifactID string) (*model.Model, *model.Artifact) {
	t.Helper()
	store, _, err := modelcat.Open("")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := modelcat.Seed(context.Background(), store); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	m, a, err := store.Artifact(context.Background(), modelID, artifactID)
	if err != nil {
		t.Fatalf("Artifact: %v", err)
	}
	return m, a
}

func engineOf(t *testing.T, engineID string) *model.EngineProfile {
	t.Helper()
	engine, err := modelcat.Engine(engineID)
	if err != nil {
		t.Fatalf("Engine: %v", err)
	}
	return engine
}

// dockerComposeConfig runs `docker compose config` on the rendered file, which
// parses it with the compose specification and reports what is wrong with it.
func dockerComposeConfig(t *testing.T, rendered string) {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not installed")
	}
	if err := exec.Command("docker", "compose", "version").Run(); err != nil {
		t.Skip("docker compose not available")
	}
	cmd := exec.Command("docker", "compose", "-p", "modelcheck", "-f", "-", "config", "--quiet")
	cmd.Stdin = strings.NewReader(rendered)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("docker compose config: %v\n%s\n--- rendered ---\n%s", err, stderr.String(), rendered)
	}
}

func TestRenderSeedCombinationsPassComposeConfig(t *testing.T) {
	for _, tc := range []struct {
		modelID, artifactID, engineID string
		gpu                           bool
		want                          []string
		wantNot                       []string
	}{
		{
			"qwen3-embedding-0.6b", "hf-bf16", model.EngineVLLM, true,
			[]string{`"--model", "Qwen/Qwen3-Embedding-0.6B"`, `"--runner", "pooling"`, "driver: nvidia",
				`"HF_TOKEN": "${HF_TOKEN:-}"`, `"vllm/vllm-openai:v0.32.0"`, `"8000:8000"`},
			nil,
		},
		{
			"ministral-3-8b", "hf-fp8", model.EngineVLLM, false,
			[]string{`"--model", "mistralai/Ministral-3-8B-Instruct-2512"`},
			[]string{"--runner", "driver: nvidia"},
		},
		{
			"qwen3-vl-8b", "ollama-gguf", model.EngineOllama, true,
			[]string{`"pull", "qwen3-vl:8b"`, "condition: service_healthy", "driver: nvidia", `"11434:11434"`},
			[]string{"HF_TOKEN"},
		},
	} {
		t.Run(tc.modelID+"/"+tc.engineID, func(t *testing.T) {
			m, a := artifactOf(t, tc.modelID, tc.artifactID)
			rendered, err := Render(m, a, engineOf(t, tc.engineID), Options{GPU: tc.gpu})
			if err != nil {
				t.Fatalf("Render: %v", err)
			}
			for _, want := range tc.want {
				if !strings.Contains(rendered, want) {
					t.Errorf("rendered file lacks %q\n%s", want, rendered)
				}
			}
			for _, unwanted := range tc.wantNot {
				if strings.Contains(rendered, unwanted) {
					t.Errorf("rendered file holds %q\n%s", unwanted, rendered)
				}
			}
			dockerComposeConfig(t, rendered)
		})
	}
}

func TestRenderEnvIsQuotedAndNotInterpolated(t *testing.T) {
	m, a := artifactOf(t, "qwen3-vl-8b", "hf-bf16")
	rendered, err := Render(m, a, engineOf(t, model.EngineVLLM), Options{Env: map[string]string{
		"HF_TOKEN":     "literal",
		"ODD_VALUE":    `a "quoted" $HOME: value # not a comment`,
		"VLLM_LOGGING": "DEBUG",
	}})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	for _, want := range []string{`"HF_TOKEN": "literal"`, `"ODD_VALUE": "a \"quoted\" $$HOME: value # not a comment"`} {
		if !strings.Contains(rendered, want) {
			t.Errorf("rendered file lacks %q\n%s", want, rendered)
		}
	}
	dockerComposeConfig(t, rendered)
}

func TestRenderRefusesIncompatiblePairs(t *testing.T) {
	vl, hf := artifactOf(t, "qwen3-vl-8b", "hf-bf16")
	_, gguf := artifactOf(t, "qwen3-vl-8b", "ollama-gguf")

	// The artifact does not list the engine.
	if _, err := Render(vl, hf, engineOf(t, model.EngineOllama), Options{}); !errors.Is(err, ErrIncompatible) {
		t.Errorf("hf on ollama: error = %v, want ErrIncompatible", err)
	}
	if _, err := Render(vl, gguf, engineOf(t, model.EngineVLLM), Options{}); !errors.Is(err, ErrIncompatible) {
		t.Errorf("gguf on vllm: error = %v, want ErrIncompatible", err)
	}

	// The artifact lists the engine, but the engine does not load the format.
	listed := *gguf
	listed.Engines = []model.EngineCompat{{Engine: model.EngineVLLM}}
	if _, err := Render(vl, &listed, engineOf(t, model.EngineVLLM), Options{}); !errors.Is(err, ErrIncompatible) {
		t.Errorf("gguf listed for vllm: error = %v, want ErrIncompatible", err)
	}
}

func TestRenderRefusesBadEnvName(t *testing.T) {
	m, a := artifactOf(t, "qwen3-vl-8b", "hf-bf16")
	_, err := Render(m, a, engineOf(t, model.EngineVLLM), Options{Env: map[string]string{"BAD NAME": "x"}})
	if !errors.Is(err, ErrInvalidOption) {
		t.Errorf("error = %v, want ErrInvalidOption", err)
	}
}

func TestCompletionServicesAreInTheRenderedFile(t *testing.T) {
	m, a := artifactOf(t, "qwen3-vl-8b", "ollama-gguf")
	rendered, err := Render(m, a, engineOf(t, model.EngineOllama), Options{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	services := CompletionServices(model.EngineOllama)
	if len(services) == 0 {
		t.Fatal("ollama has no completion service; the pull would not be waited for")
	}
	for _, name := range services {
		if !strings.Contains(rendered, "\n  "+name+":\n") {
			t.Errorf("service %q is not in the rendered ollama file", name)
		}
	}
	if got := CompletionServices(model.EngineVLLM); len(got) != 0 {
		t.Errorf("vllm completion services = %v, want none", got)
	}
}
