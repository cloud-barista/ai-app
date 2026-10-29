// Package compose renders the docker-compose.yml that runs a model service: an
// inference engine container loading one model artifact.
//
// The file is meant for one node and is started with a single
// `docker compose up -d` in the directory it is placed in. Values that come
// from a request are written as quoted YAML scalars with "$" doubled, so an
// unusual value can neither break the YAML nor trigger compose interpolation.
package compose

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"text/template"

	"github.com/ish-hcc/ai-agent-proto/internal/model"
)

// ErrIncompatible is returned when an artifact cannot be run with an engine.
var ErrIncompatible = errors.New("artifact is not compatible with the engine")

// ErrInvalidOption is returned when a render option cannot be written into a
// compose file.
var ErrInvalidOption = errors.New("invalid compose option")

// envNamePattern is what an environment variable name may look like.
var envNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// hfTokenEnv is the variable vLLM reads a Hugging Face token from. Gated
// repositories need it; the value is left to the .env file beside the compose
// file so that it never appears in the rendered file itself.
const hfTokenEnv = "HF_TOKEN"

// Options are the per-deployment choices of a render.
type Options struct {
	// GPU reserves every NVIDIA GPU of the node for the engine container.
	GPU bool
	// Env is added to the engine container environment. A key that the
	// template also sets replaces the template value.
	Env map[string]string
}

// envVar is one rendered environment entry.
type envVar struct {
	Name  string
	Value string
}

// view is what a template sees.
type view struct {
	Model    *model.Model
	Artifact *model.Artifact
	Engine   *model.EngineProfile
	GPU      bool
	Args     []string
	Env      []envVar
}

// Render builds the docker-compose.yml for artifact of m run with engine.
//
// It refuses the pairing unless the artifact lists the engine and the engine
// loads the artifact format.
func Render(m *model.Model, artifact *model.Artifact, engine *model.EngineProfile, opts Options) (string, error) {
	if !artifact.SupportsEngine(engine.ID) {
		return "", fmt.Errorf("%w: artifact %s/%s does not list engine %s", ErrIncompatible, m.ID, artifact.ID, engine.ID)
	}
	if !engine.LoadsFormat(artifact.Format) {
		return "", fmt.Errorf("%w: engine %s does not load format %s", ErrIncompatible, engine.ID, artifact.Format)
	}

	tmpl, ok := templates[engine.ID]
	if !ok {
		return "", fmt.Errorf("%w: no compose template for engine %s", ErrIncompatible, engine.ID)
	}

	v := view{Model: m, Artifact: artifact, Engine: engine, GPU: opts.GPU}
	defaults := map[string]string{}
	switch engine.ID {
	case model.EngineVLLM:
		v.Args = vllmArgs(m, artifact, engine)
		defaults[hfTokenEnv] = "${" + hfTokenEnv + ":-}"
	}
	env, err := mergeEnv(defaults, opts.Env)
	if err != nil {
		return "", err
	}
	v.Env = env

	var out bytes.Buffer
	if err := tmpl.Execute(&out, v); err != nil {
		return "", fmt.Errorf("failed to render compose file: %w", err)
	}
	return out.String(), nil
}

// vllmArgs are the arguments after the image entrypoint, `vllm serve`.
//
// An embedding model is started with --runner pooling. vLLM picks the runner
// from the architecture by default, and an embedding model built on a causal
// language model architecture (Qwen3-Embedding is Qwen3ForCausalLM) would
// otherwise come up as a text generator.
func vllmArgs(m *model.Model, artifact *model.Artifact, engine *model.EngineProfile) []string {
	args := []string{"--model", artifact.Ref, "--port", fmt.Sprint(engine.Port)}
	if m.HasTask("embedding") {
		args = append(args, "--runner", "pooling")
	}
	return args
}

// mergeEnv lays extra over defaults and orders the result by name, so the same
// request always renders the same file.
func mergeEnv(defaults, extra map[string]string) ([]envVar, error) {
	merged := make(map[string]string, len(defaults)+len(extra))
	for name, value := range defaults {
		merged[name] = value
	}
	for name, value := range extra {
		if !envNamePattern.MatchString(name) {
			return nil, fmt.Errorf("%w: environment variable name %q must match [A-Za-z_][A-Za-z0-9_]*", ErrInvalidOption, name)
		}
		merged[name] = escapeDollar(value)
	}
	out := make([]envVar, 0, len(merged))
	for name, value := range merged {
		out = append(out, envVar{Name: name, Value: value})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// escapeDollar keeps compose from interpolating a literal value.
func escapeDollar(value string) string {
	return strings.ReplaceAll(value, "$", "$$")
}

// quote writes s as a double quoted YAML scalar. JSON string syntax is a
// subset of YAML double quoted syntax.
func quote(s string) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return strings.TrimSuffix(buf.String(), "\n")
}

// quoteLiteral quotes a value that compose must not interpolate.
func quoteLiteral(s string) string {
	return quote(escapeDollar(s))
}

var funcs = template.FuncMap{
	"q":  quote,
	"ql": quoteLiteral,
}

// gpuBlock reserves every NVIDIA GPU of the node. The node needs the NVIDIA
// driver and nvidia-container-toolkit.
const gpuBlock = `{{define "gpu"}}{{if .GPU}}
    deploy:
      resources:
        reservations:
          devices:
            - driver: nvidia
              count: all
              capabilities: [gpu]{{end}}{{end}}`

const envBlock = `{{define "env"}}{{if .Env}}
    environment:{{range .Env}}
      {{q .Name}}: {{q .Value}}{{end}}{{end}}{{end}}`

// vllmTemplate runs the OpenAI compatible vLLM server. The Hugging Face cache
// is a named volume, so a restart does not download the weights again. vLLM
// shares tensors between processes through shared memory, which the default
// container /dev/shm is too small for, hence ipc: host.
const vllmTemplate = `# Model service: {{.Model.ID}} ({{.Artifact.ID}}) on {{.Engine.ID}}.
# Generated by the AI-App service. HF_TOKEN is read from the environment or
# from a .env file next to this file.
services:
  engine:
    image: {{ql .Engine.Image}}
    restart: unless-stopped
    command: [{{range $i, $a := .Args}}{{if $i}}, {{end}}{{ql $a}}{{end}}]
    ports:
      - "{{.Engine.Port}}:{{.Engine.Port}}"
    ipc: host{{template "env" .}}
    volumes:
      - hf-cache:/root/.cache/huggingface{{template "gpu" .}}

volumes:
  hf-cache:
`

// ollamaTemplate runs the Ollama server and, once it answers, a one-shot
// container that pulls the model into the server and exits. Both start from
// one `docker compose up -d`; the pull container is not restarted, and pulling
// a model that is already there finishes at once.
const ollamaTemplate = `# Model service: {{.Model.ID}} ({{.Artifact.ID}}) on {{.Engine.ID}}.
# Generated by the AI-App service. The model-pull container exits after the
# model is pulled; that is expected.
services:
  engine:
    image: {{ql .Engine.Image}}
    restart: unless-stopped
    ports:
      - "{{.Engine.Port}}:{{.Engine.Port}}"{{template "env" .}}
    volumes:
      - ollama:/root/.ollama
    healthcheck:
      test: ["CMD", "ollama", "list"]
      interval: 5s
      timeout: 5s
      retries: 60{{template "gpu" .}}

  model-pull:
    image: {{ql .Engine.Image}}
    restart: "no"
    depends_on:
      engine:
        condition: service_healthy
    environment:
      OLLAMA_HOST: "http://engine:{{.Engine.Port}}"
    command: ["pull", {{ql .Artifact.Ref}}]

volumes:
  ollama:
`

var templates = func() map[string]*template.Template {
	parse := func(name, text string) *template.Template {
		t := template.Must(template.New(name).Funcs(funcs).Parse(text))
		template.Must(t.Parse(gpuBlock))
		template.Must(t.Parse(envBlock))
		return t
	}
	return map[string]*template.Template{
		model.EngineVLLM:   parse(model.EngineVLLM, vllmTemplate),
		model.EngineOllama: parse(model.EngineOllama, ollamaTemplate),
	}
}()
