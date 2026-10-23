package catalog

import (
	"context"
	"strings"
	"testing"
)

// resolveCases are instructions written the way an operator writes them, not
// the way the catalog stores them. Each names the application that instruction
// is asking for, or "" when the catalog holds nothing that fits.
//
// The set is deliberately mixed: registered ids, English model names, Korean
// transliterations, runtime names, capability words, and one request for a model
// nobody registered. A set made only of exact names would measure nothing.
var resolveCases = []struct {
	intent string
	want   string
}{
	{"vllm-llama31-8b 배포해줘", "vllm-llama31-8b"},
	{"Llama 3.1 8B 추론 서버를 GPU 한 장짜리 노드에 올려줘", "vllm-llama31-8b"},
	{"라마 추론서버 올려줘", "vllm-llama31-8b"},
	{"vLLM 로 라마 띄워", "vllm-llama31-8b"},
	{"meta-llama/Llama-3.1-8B-Instruct 배포", "vllm-llama31-8b"},
	{"OpenAI 호환 엔드포인트 하나 띄워줘", "vllm-llama31-8b"},
	{"Qwen2.5 7B 배포", "ollama-qwen25-7b"},
	{"큐원 모델 올려줘", "ollama-qwen25-7b"},
	{"ollama 띄워줘", "ollama-qwen25-7b"},
	{"qwen2.5:7b", "ollama-qwen25-7b"},
	{"경량 모델로 스모크 테스트 하나 돌려보자", "ollama-qwen25-7b"},
	{"Triton 으로 ResNet 서빙해줘", "triton-resnet50"},
	{"resnet50 배포", "triton-resnet50"},
	{"트리톤 올려", "triton-resnet50"},
	{"이미지 분류 모델 하나 배포해줘", "triton-resnet50"},
	{"onnx 모델 서빙", "triton-resnet50"},
	{"비전 추론 워크로드", "triton-resnet50"},
	{"gpt-4 배포해줘", ""},
	{"클러스터 상태 알려줘", ""},
}

// baselineResolve is what the catalog offered before Resolve existed: the id and
// the name, matched as substrings. It is kept in the test only, as the number the
// new matcher has to beat.
func baselineResolve(store *Store, query string) string {
	lowered := strings.ToLower(query)
	for _, spec := range store.List(context.Background()) {
		if strings.Contains(lowered, strings.ToLower(spec.ID)) ||
			strings.Contains(lowered, strings.ToLower(spec.Name)) {
			return spec.ID
		}
	}
	return ""
}

func seededStore(t *testing.T) *Store {
	t.Helper()
	store := NewStore()
	if err := Seed(context.Background(), store); err != nil {
		t.Fatalf("seed catalog: %v", err)
	}
	return store
}

func TestResolveTopOneAccuracy(t *testing.T) {
	store := seededStore(t)
	ctx := context.Background()

	var matcherHits, baselineHits int
	for _, tc := range resolveCases {
		got := ""
		if matches := store.Resolve(ctx, tc.intent, 3); len(matches) > 0 {
			got = matches[0].AppID
		}
		if got == tc.want {
			matcherHits++
		} else {
			t.Logf("miss  %-46q want=%-18q got=%q", tc.intent, tc.want, got)
		}
		if baselineResolve(store, tc.intent) == tc.want {
			baselineHits++
		}
	}

	total := len(resolveCases)
	t.Logf("top-1 accuracy: matcher %d/%d, substring baseline %d/%d", matcherHits, total, baselineHits, total)

	if matcherHits <= baselineHits {
		t.Errorf("matcher %d/%d does not beat the substring baseline %d/%d", matcherHits, total, baselineHits, total)
	}
	if matcherHits < total {
		t.Errorf("top-1 accuracy %d/%d, want %d/%d", matcherHits, total, total, total)
	}
}

// TestResolveRejectsUnknown guards the case that costs the most: answering with
// a confident wrong application when the catalog holds nothing that fits.
func TestResolveRejectsUnknown(t *testing.T) {
	store := seededStore(t)
	for _, intent := range []string{"gpt-4 배포해줘", "클러스터 상태 알려줘", ""} {
		if matches := store.Resolve(context.Background(), intent, 3); len(matches) != 0 {
			t.Errorf("intent %q resolved to %v, want no candidate", intent, matches)
		}
	}
}

// TestResolveRanksExactIDFirst keeps an explicit id from being outranked by a
// pile of weak word matches on another application.
func TestResolveRanksExactIDFirst(t *testing.T) {
	store := seededStore(t)
	matches := store.Resolve(context.Background(), "triton-resnet50 말고 vllm-llama31-8b 로 해줘", 3)
	if len(matches) == 0 {
		t.Fatal("no candidate for an instruction naming two ids")
	}
	if matches[0].Score != matches[1].Score {
		t.Errorf("two named ids should tie on the id signal, got %d and %d", matches[0].Score, matches[1].Score)
	}
}
