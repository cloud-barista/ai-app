package compose

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"reflect"
	"regexp"
	"slices"
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

func engineOf(t *testing.T, engineID string) *model.RuntimeProfile {
	t.Helper()
	engine, err := modelcat.Runtime(engineID)
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
			"qwen3-embedding-0.6b", "hf-bf16", model.RuntimeVLLM, true,
			[]string{`"--model", "Qwen/Qwen3-Embedding-0.6B"`, `"--runner", "pooling"`, "driver: nvidia",
				`"HF_TOKEN": "${HF_TOKEN:-}"`, `"vllm/vllm-openai:v0.32.0"`, `"8000:8000"`},
			nil,
		},
		{
			"ministral-3-8b", "hf-fp8", model.RuntimeVLLM, false,
			[]string{`"--model", "mistralai/Ministral-3-8B-Instruct-2512"`},
			[]string{"--runner", "driver: nvidia"},
		},
		{
			"qwen3-vl-8b", "ollama-gguf", model.RuntimeOllama, true,
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
	rendered, err := Render(m, a, engineOf(t, model.RuntimeVLLM), Options{Env: map[string]string{
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
	if _, err := Render(vl, hf, engineOf(t, model.RuntimeOllama), Options{}); !errors.Is(err, ErrIncompatible) {
		t.Errorf("hf on ollama: error = %v, want ErrIncompatible", err)
	}
	if _, err := Render(vl, gguf, engineOf(t, model.RuntimeVLLM), Options{}); !errors.Is(err, ErrIncompatible) {
		t.Errorf("gguf on vllm: error = %v, want ErrIncompatible", err)
	}

	// The artifact lists the engine, but the engine does not load the format.
	listed := *gguf
	listed.Runtimes = []model.RuntimeCompat{{RuntimeID: model.RuntimeVLLM}}
	if _, err := Render(vl, &listed, engineOf(t, model.RuntimeVLLM), Options{}); !errors.Is(err, ErrIncompatible) {
		t.Errorf("gguf listed for vllm: error = %v, want ErrIncompatible", err)
	}
}

func TestRenderRefusesBadEnvName(t *testing.T) {
	m, a := artifactOf(t, "qwen3-vl-8b", "hf-bf16")
	_, err := Render(m, a, engineOf(t, model.RuntimeVLLM), Options{Env: map[string]string{"BAD NAME": "x"}})
	if !errors.Is(err, ErrInvalidOption) {
		t.Errorf("error = %v, want ErrInvalidOption", err)
	}
}

func TestCompletionServicesAreInTheRenderedFile(t *testing.T) {
	m, a := artifactOf(t, "qwen3-vl-8b", "ollama-gguf")
	rendered, err := Render(m, a, engineOf(t, model.RuntimeOllama), Options{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	services := CompletionServices(model.RuntimeOllama)
	if len(services) == 0 {
		t.Fatal("ollama has no completion service; the pull would not be waited for")
	}
	for _, name := range services {
		if !strings.Contains(rendered, "\n  "+name+":\n") {
			t.Errorf("service %q is not in the rendered ollama file", name)
		}
	}
	if got := CompletionServices(model.RuntimeVLLM); len(got) != 0 {
		t.Errorf("vllm completion services = %v, want none", got)
	}
}

func TestGPUCheckOfEachRuntime(t *testing.T) {
	if got := GPUCheckFor(model.RuntimeVLLM); got == nil || got.Method != GPUCheckNvidiaSMI {
		t.Errorf("vllm gpu check = %+v, want nvidia-smi", got)
	}
	check := GPUCheckFor(model.RuntimeOllama)
	if check == nil || check.Method != GPUCheckLog {
		t.Fatalf("ollama gpu check = %+v, want log", check)
	}
	m, a := artifactOf(t, "qwen3-vl-8b", "ollama-gguf")
	rendered, err := Render(m, a, engineOf(t, model.RuntimeOllama), Options{GPU: true})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(rendered, "\n  "+check.Service+":\n    image: ") {
		t.Errorf("service %q is not the ollama server of the rendered file", check.Service)
	}
	pattern, evidence := regexp.MustCompile(check.Pattern), regexp.MustCompile(check.Evidence)
	// The first line is from a Tesla T4 node, the second from
	// ollama/ollama:0.40.13 on a host whose docker has no NVIDIA runtime.
	gpuLine := `time=2026-11-03T08:00:00.000Z level=INFO source=types.go:50 msg="inference compute" id=0 library=CUDA compute=7.5 description="Tesla T4" total="15.6 GiB"`
	cpuLine := `time=2026-11-03T08:49:00.128Z level=INFO source=types.go:50 msg="inference compute" id=cpu library=cpu compute="" name=cpu description=cpu libdirs=ollama driver="" pci_id="" type="" total="53.6 GiB" available="53.5 GiB"`
	if !pattern.MatchString(gpuLine) || pattern.MatchString(cpuLine) {
		t.Errorf("pattern %q must match the CUDA line only", check.Pattern)
	}
	if !evidence.MatchString(cpuLine) {
		t.Errorf("evidence %q must pick the CPU line to show", check.Evidence)
	}
}

func TestRenderAppendsArgsAfterBuiltins(t *testing.T) {
	m, a := artifactOf(t, "qwen3-vl-8b", "hf-bf16")
	args := []string{"--max-model-len", "32768", `--odd=a "q" $HOME: # é,[x]{y}`}
	rendered, err := Render(m, a, engineOf(t, model.RuntimeVLLM), Options{Args: args})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	want := `command: ["--model", "Qwen/Qwen3-VL-8B-Instruct", "--port", "8000", ` +
		`"--max-model-len", "32768", "--odd=a \"q\" $$HOME: # é,[x]{y}"]`
	if !strings.Contains(rendered, want) {
		t.Errorf("rendered file lacks %q\n%s", want, rendered)
	}
	dockerComposeConfig(t, rendered)
	if got := composeCommand(t, rendered); !reflect.DeepEqual(got, append([]string{
		"--model", "Qwen/Qwen3-VL-8B-Instruct", "--port", "8000"}, args...)) {
		t.Errorf("docker compose reads command %q", got)
	}
}

func TestRenderPutsCatalogArgsBeforeRequestArgs(t *testing.T) {
	m, a := artifactOf(t, "qwen3-vl-8b", "hf-bf16")
	a.Runtimes = []model.RuntimeCompat{{RuntimeID: model.RuntimeVLLM, Args: []string{"--max-model-len", "16384"}}}
	builtin := []string{"--model", "Qwen/Qwen3-VL-8B-Instruct", "--port", "8000"}

	for name, tc := range map[string]struct {
		request []string
		want    []string
	}{
		"catalog only":        {nil, append(slices.Clone(builtin), "--max-model-len", "16384")},
		"catalog and request": {[]string{"--max-model-len", "8192"}, append(slices.Clone(builtin), "--max-model-len", "16384", "--max-model-len", "8192")},
	} {
		rendered, err := Render(m, a, engineOf(t, model.RuntimeVLLM), Options{Args: tc.request})
		if err != nil {
			t.Fatalf("%s: Render: %v", name, err)
		}
		if got := composeCommand(t, rendered); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: docker compose reads command %q, want %q", name, got, tc.want)
		}
	}
}

func TestRenderSeedMinistralCarriesCatalogArgs(t *testing.T) {
	m, a := artifactOf(t, "ministral-3-8b", "hf-fp8")
	rendered, err := Render(m, a, engineOf(t, model.RuntimeVLLM), Options{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	want := `command: ["--model", "mistralai/Ministral-3-8B-Instruct-2512", "--port", "8000", ` +
		`"--max-model-len", "32768", "--tokenizer-mode", "mistral", "--config-format", "mistral", "--load-format", "mistral"]`
	if !strings.Contains(rendered, want) {
		t.Errorf("rendered file lacks %q\n%s", want, rendered)
	}
}

func TestRenderRefusesArgs(t *testing.T) {
	vllmModel, vllmArtifact := artifactOf(t, "ministral-3-8b", "hf-fp8")
	tooMany := make([]string, model.MaxRuntimeArgs+1)
	for i := range tooMany {
		tooMany[i] = "x"
	}
	for name, args := range map[string][]string{
		"too many":   tooMany,
		"newline":    {"--max-model-len\n32768"},
		"control":    {"a\tb"},
		"empty":      {""},
		"too long":   {strings.Repeat("x", 257)},
		"model":      {"--model", "other/model"},
		"model=":     {"--model=other/model"},
		"port":       {"--port", "9000"},
		"host":       {"--host=0.0.0.0"},
		"prefix":     {"--mod", "other/model"},
		"end of opt": {"--", "other/model"},
	} {
		if _, err := Render(vllmModel, vllmArtifact, engineOf(t, model.RuntimeVLLM), Options{Args: args}); !errors.Is(err, model.ErrValidation) {
			t.Errorf("%s: error = %v, want ErrValidation", name, err)
		}
	}
	if _, err := Render(vllmModel, vllmArtifact, engineOf(t, model.RuntimeVLLM), Options{Args: []string{strings.Repeat("é", 256), "--model-impl", "vllm"}}); err != nil {
		t.Errorf("256 characters and --model-impl: %v", err)
	}

	m, a := artifactOf(t, "qwen3-vl-8b", "ollama-gguf")
	_, err := Render(m, a, engineOf(t, model.RuntimeOllama), Options{Args: []string{"--max-model-len", "32768"}})
	if !errors.Is(err, ErrInvalidOption) || !strings.Contains(err.Error(), "args are not supported by runtime ollama") {
		t.Errorf("ollama: error = %v, want ErrInvalidOption about ollama", err)
	}
}

// composeCommand is the command of the engine service as docker compose reads
// the rendered file. The config output writes a literal "$" as "$$" again.
func composeCommand(t *testing.T, rendered string) []string {
	t.Helper()
	cmd := exec.Command("docker", "compose", "-p", "modelcheck", "-f", "-", "config", "--format", "json")
	cmd.Stdin = strings.NewReader(rendered)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("docker compose config: %v", err)
	}
	var cfg struct {
		Services map[string]struct {
			Command []string `json:"command"`
		} `json:"services"`
	}
	if err := json.Unmarshal(out, &cfg); err != nil {
		t.Fatalf("decode compose config: %v", err)
	}
	command := cfg.Services["engine"].Command
	for i := range command {
		command[i] = strings.ReplaceAll(command[i], "$$", "$")
	}
	return command
}

func TestRenderGivesTheAPIKeyAsEnvNotAsArgument(t *testing.T) {
	m, a := artifactOf(t, "qwen3-embedding-0.6b", "hf-bf16")
	const key = "k3y-Of.Test~0123456789"
	rendered, err := Render(m, a, engineOf(t, model.RuntimeVLLM), Options{APIKey: key, Env: map[string]string{"A_FIRST": "1"}})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(rendered, `"VLLM_API_KEY": "`+key+`"`) {
		t.Errorf("rendered file lacks the VLLM_API_KEY entry\n%s", rendered)
	}
	command := regexp.MustCompile(`(?m)^\s+command: .*$`).FindString(rendered)
	if command == "" || strings.Contains(command, key) || strings.Contains(command, "--api-key") {
		t.Errorf("command = %q, want one without the key", command)
	}
	if strings.Count(rendered, key) != 1 {
		t.Errorf("the key shows %d times, want once\n%s", strings.Count(rendered, key), rendered)
	}
	if i, j := strings.Index(rendered, `"A_FIRST"`), strings.Index(rendered, `"VLLM_API_KEY"`); i < 0 || j < i {
		t.Errorf("environment is not in name order\n%s", rendered)
	}
	dockerComposeConfig(t, rendered)

	without, err := Render(m, a, engineOf(t, model.RuntimeVLLM), Options{})
	if err != nil || strings.Contains(without, APIKeyEnv) {
		t.Errorf("without a key: %v\n%s", err, without)
	}
}

func TestRenderRefusesAnAPIKeyItCannotGive(t *testing.T) {
	m, a := artifactOf(t, "qwen3-vl-8b", "ollama-gguf")
	if _, err := Render(m, a, engineOf(t, model.RuntimeOllama), Options{APIKey: "0123456789abcdef"}); !errors.Is(err, ErrInvalidOption) {
		t.Errorf("ollama with a key: error = %v, want ErrInvalidOption", err)
	}
	m, a = artifactOf(t, "qwen3-embedding-0.6b", "hf-bf16")
	if _, err := Render(m, a, engineOf(t, model.RuntimeVLLM), Options{Env: map[string]string{APIKeyEnv: "x"}}); !errors.Is(err, ErrInvalidOption) {
		t.Errorf("vllm with %s in env: error = %v, want ErrInvalidOption", APIKeyEnv, err)
	}
}
