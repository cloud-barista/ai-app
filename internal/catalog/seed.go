package catalog

import (
	"context"

	"github.com/rs/zerolog/log"

	"github.com/ish-hcc/ai-agent-proto/internal/model"
)

// Seed registers the built-in application specs.
//
// They exist so the agent has something to reason about on a fresh start, and so
// the metadata specification has worked examples rather than only a struct.
// Each entry keeps runtime and accelerator as independent axes: swapping a GPU
// requirement for an NPU one does not change the runtime field.
func Seed(ctx context.Context, store *Store) error {
	for _, spec := range builtinApps() {
		if _, err := store.Register(ctx, spec); err != nil {
			return err
		}
	}
	log.Info().Int("count", len(builtinApps())).Msg("Seeded AI application catalog")
	return nil
}

func builtinApps() []model.AppSpec {
	return []model.AppSpec{
		{
			ID:          "vllm-llama31-8b",
			Name:        "vLLM Llama 3.1 8B Instruct",
			Version:     "0.1.0",
			Description: "OpenAI compatible chat completion endpoint served by vLLM",
			Runtime:     model.RuntimeVLLM,
			RuntimeVer:  "0.6.3",
			Model: model.ModelRef{
				ID:      "meta-llama/Llama-3.1-8B-Instruct",
				Format:  "safetensors",
				SizeGiB: 16,
			},
			Accelerator: model.AcceleratorRequirement{
				Type:         "gpu",
				MinCount:     1,
				MinMemoryGiB: 22,
			},
			Resources: model.ResourceRequirement{
				MinVCPU:      8,
				MinMemoryGiB: 32,
				RootDiskGiB:  120,
			},
			Serving: model.ServingSpec{Port: 8000, HealthPath: "/health", Protocol: "http"},
			Install: model.InstallSpec{
				UserName:       "cb-user",
				TimeoutMinutes: 45,
				Commands: []string{
					"sudo apt-get update -y",
					"sudo apt-get install -y python3-pip",
					"python3 -m pip install --upgrade pip",
					"python3 -m pip install vllm==0.6.3",
					"nohup python3 -m vllm.entrypoints.openai.api_server --model meta-llama/Llama-3.1-8B-Instruct --port 8000 > /tmp/vllm.log 2>&1 &",
				},
			},
			DeployTarget: []model.DeployTarget{model.DeployTargetVM},
			Aliases:      []string{"llama", "llama3", "llama3.1", "라마", "라마3", "vllm", "openai"},
			Labels:       map[string]string{"framework": "ai-mcmp", "kind": "ai-app"},
		},
		{
			ID:          "ollama-qwen25-7b",
			Name:        "Ollama Qwen2.5 7B",
			Version:     "0.1.0",
			Description: "Small footprint serving endpoint for smoke testing a new accelerator",
			Runtime:     model.RuntimeOllama,
			RuntimeVer:  "0.5.4",
			Model: model.ModelRef{
				ID:      "qwen2.5:7b",
				Format:  "gguf",
				SizeGiB: 5,
			},
			Accelerator: model.AcceleratorRequirement{
				Type:         "gpu",
				MinCount:     1,
				MinMemoryGiB: 8,
			},
			Resources: model.ResourceRequirement{
				MinVCPU:      4,
				MinMemoryGiB: 16,
				RootDiskGiB:  60,
			},
			Serving: model.ServingSpec{Port: 11434, HealthPath: "/", Protocol: "http"},
			Install: model.InstallSpec{
				UserName:       "cb-user",
				TimeoutMinutes: 30,
				Commands: []string{
					"curl -fsSL https://ollama.com/install.sh | sh",
					"sudo systemctl enable --now ollama",
					"ollama pull qwen2.5:7b",
				},
			},
			DeployTarget: []model.DeployTarget{model.DeployTargetVM},
			Aliases:      []string{"qwen", "qwen2.5", "큐원", "콰인", "ollama", "올라마", "경량"},
			Labels:       map[string]string{"framework": "ai-mcmp", "kind": "ai-app"},
		},
		{
			ID:          "triton-resnet50",
			Name:        "Triton ResNet-50",
			Version:     "0.1.0",
			Description: "Vision inference endpoint used to exercise a non LLM workload path",
			Runtime:     model.RuntimeTriton,
			RuntimeVer:  "24.09",
			Model: model.ModelRef{
				ID:      "resnet50",
				Format:  "onnx",
				SizeGiB: 1,
			},
			Accelerator: model.AcceleratorRequirement{
				Type:         "gpu",
				MinCount:     1,
				MinMemoryGiB: 8,
			},
			Resources: model.ResourceRequirement{
				MinVCPU:      4,
				MinMemoryGiB: 16,
				RootDiskGiB:  60,
			},
			Serving: model.ServingSpec{Port: 8000, HealthPath: "/v2/health/ready", Protocol: "http"},
			Install: model.InstallSpec{
				UserName:       "cb-user",
				TimeoutMinutes: 30,
				Commands: []string{
					"sudo apt-get update -y",
					"sudo apt-get install -y docker.io",
					"sudo docker run -d --gpus all -p 8000:8000 -p 8001:8001 -p 8002:8002 nvcr.io/nvidia/tritonserver:24.09-py3 tritonserver --model-repository=/models",
				},
			},
			DeployTarget: []model.DeployTarget{model.DeployTargetVM},
			Aliases:      []string{"resnet", "resnet50", "triton", "트리톤", "레스넷", "vision", "비전", "이미지 분류", "분류"},
			Labels:       map[string]string{"framework": "ai-mcmp", "kind": "ai-app"},
		},
	}
}
