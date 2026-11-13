package observation

import "context"

// KV cache is optional because older vLLM exporters do not expose it.
func (p *Prometheus) addKVCache(ctx context.Context, labels map[string]string, w Window, out *Metrics) {
	lines, issue := p.read(ctx, w, "vllm", labels, []string{"vllm:kv_cache_usage_perc"})
	if issue != "" || len(lines) == 0 {
		return
	}
	cache := summarize(lines, w, "prometheus/vllm", "ratio", 1, false)
	if cache.Quality == "measured" {
		out.Serving["kvCacheUsage"] = cache.value(cache.mean)
	}
}
