package observation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	latencyBuckets = "vllm:e2e_request_latency_seconds_bucket"
	generation     = "vllm:generation_tokens_total"
	finished       = "vllm:request_success_total"
	waiting        = "vllm:num_requests_waiting"
	gpuUtil        = "DCGM_FI_DEV_GPU_UTIL"
	gpuUsed        = "DCGM_FI_DEV_FB_USED"
	gpuFree        = "DCGM_FI_DEV_FB_FREE"
)

type Prometheus struct {
	endpoint string
	token    string
	client   *http.Client
}

func NewPrometheus(baseURL, token string) (*Prometheus, error) {
	u, err := url.Parse(baseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" ||
		u.User != nil || u.ForceQuery || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("observation Prometheus URL must be a base HTTP or HTTPS URL")
	}
	return &Prometheus{
		endpoint: strings.TrimRight(u.String(), "/") + "/api/v1/query",
		token:    token,
		client: &http.Client{
			Timeout: 5 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

type series struct {
	Labels map[string]string    `json:"metric"`
	Values [][2]json.RawMessage `json:"values"`
}

// Read requests raw range vectors to retain their scrape timestamps.
func (p *Prometheus) Read(ctx context.Context, target Placement, window Window) Metrics {
	labels := map[string]string{"nsId": target.Namespace, "infraId": target.InfraID, "nodeId": target.NodeID}
	gpus, gpuIssue := p.read(ctx, window, "dcgm", labels, []string{
		gpuUtil, gpuUsed, gpuFree, gpuUsed + "_BYTES", gpuFree + "_BYTES",
	})
	labels["deploymentId"] = target.DeploymentID
	serving, servingIssue := p.read(ctx, window, "vllm", labels, []string{latencyBuckets, generation, finished, waiting})

	out := Metrics{
		Serving: summarizeServing(serving, window, servingIssue),
		GPUs:    summarizeGPUs(gpus, window),
		Issues:  []string{},
	}
	if len(out.GPUs) == 0 && gpuIssue == "" {
		gpuIssue = "missing"
	}
	if gpuIssue != "" {
		out.Issues = append(out.Issues, "dcgm:"+gpuIssue)
	}
	if servingIssue != "" {
		out.Issues = append(out.Issues, "vllm:"+servingIssue)
	}
	out.Complete = len(out.Issues) == 0 && allMeasured(out.Serving)
	for _, device := range out.GPUs {
		out.Complete = out.Complete && allMeasured(device)
	}
	return out
}

func (p *Prometheus) read(ctx context.Context, w Window, job string, labels map[string]string, names []string) ([]series, string) {
	match := []string{`__name__=~` + strconv.Quote(strings.Join(names, "|")), `job=` + strconv.Quote(job)}
	for _, key := range []string{"nsId", "infraId", "nodeId", "deploymentId"} {
		if value := labels[key]; value != "" {
			match = append(match, key+"="+strconv.Quote(value))
		}
	}
	query := url.Values{
		"query": {fmt.Sprintf("{%s}[%ds]", strings.Join(match, ","), int(w.End.Sub(w.Start)/time.Second))},
		"time":  {w.End.UTC().Format(time.RFC3339)},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.endpoint+"?"+query.Encode(), nil)
	if err != nil {
		return nil, "unavailable"
	}
	req.Header.Set("Accept", "application/json")
	if p.token != "" {
		req.Header.Set("Authorization", "Bearer "+p.token)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, "unavailable"
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "unavailable"
	}
	var payload struct {
		Status   string   `json:"status"`
		Warnings []string `json:"warnings"`
		Data     struct {
			Type   string   `json:"resultType"`
			Result []series `json:"result"`
		} `json:"data"`
	}
	dec := json.NewDecoder(io.LimitReader(resp.Body, 4<<20))
	if err := dec.Decode(&payload); err != nil || dec.Decode(new(any)) != io.EOF ||
		payload.Status != "success" || len(payload.Warnings) != 0 || payload.Data.Type != "matrix" {
		return nil, "invalid_response"
	}
	for _, line := range payload.Data.Result {
		if line.Labels["job"] != job || !slices.Contains(names, line.Labels["__name__"]) ||
			(job == "dcgm" && line.Labels["UUID"] == "") {
			return nil, "label_mismatch"
		}
		for key, value := range labels {
			if line.Labels[key] != value {
				return nil, "label_mismatch"
			}
		}
	}
	return payload.Data.Result, ""
}
