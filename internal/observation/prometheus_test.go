package observation

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestPrometheusObservationBindsSeriesAndPreservesQuality(t *testing.T) {
	start := time.Date(2026, 11, 10, 1, 0, 0, 0, time.UTC)
	w := Window{Start: start, End: start.Add(3 * time.Minute)}
	target := Placement{Namespace: "etri", InfraID: "travel", NodeID: "vm1", DeploymentID: "deployment-1"}
	for _, mode := range []string{"complete", "wrong-node", "stale", "missing-gpu", "reset"} {
		t.Run(mode, func(t *testing.T) {
			fixture := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
				query := r.URL.Query().Get("query")
				if r.URL.Path != "/api/v1/query" || r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer sample" ||
					r.URL.Query().Get("time") != w.End.Format(time.RFC3339) || !strings.HasSuffix(query, "[180s]") {
					t.Errorf("unexpected request %s %s", r.Method, r.URL)
				}
				for _, match := range []string{`nsId="etri"`, `infraId="travel"`, `nodeId="vm1"`} {
					if !strings.Contains(query, match) {
						t.Errorf("query %q missing %s", query, match)
					}
				}
				job := "dcgm"
				if strings.Contains(query, `job="vllm"`) {
					job = "vllm"
					if !strings.Contains(query, `deploymentId="deployment-1"`) {
						t.Errorf("inference query is not tied to deployment: %s", query)
					}
				}
				var lines []series
				add := func(name string, fields map[string]string, initial, step int64) {
					s := series{Labels: map[string]string{
						"__name__": name, "job": job, "nsId": "etri", "infraId": "travel", "nodeId": "vm1",
					}}
					for key, value := range fields {
						s.Labels[key] = value
					}
					if job == "vllm" {
						s.Labels["deploymentId"] = target.DeploymentID
					}
					if mode == "wrong-node" && job == "dcgm" {
						s.Labels["nodeId"] = "vm2"
					}
					for i := int64(1); i <= 6; i++ {
						if mode == "stale" && name == gpuUtil && i > 4 {
							break
						}
						value := initial + i*step
						if mode == "reset" && name == generation && i == 6 {
							value = 0
						}
						s.Values = append(s.Values, [2]json.RawMessage{
							json.RawMessage(strconv.FormatInt(start.Unix()+30*i, 10)),
							json.RawMessage(strconv.Quote(strconv.FormatInt(value, 10))),
						})
					}
					lines = append(lines, s)
				}
				if job == "vllm" {
					add(latencyBuckets, map[string]string{"le": "0.5"}, 100, 15)
					add(latencyBuckets, map[string]string{"le": "1"}, 120, 20)
					add(latencyBuckets, map[string]string{"le": "+Inf"}, 120, 20)
					add(generation, nil, 1000, 300)
					add(waiting, nil, 2, 0)
					add(finished, map[string]string{"finished_reason": "error"}, 0, 1)
					add(finished, map[string]string{"finished_reason": "abort"}, 0, 0)
				} else if mode != "missing-gpu" {
					gpu := map[string]string{"UUID": "GPU-1"}
					add(gpuUtil, gpu, 50, 0)
					add(gpuUsed+"_BYTES", gpu, 4096<<20, 1024<<20)
					add(gpuFree, gpu, 12000, -1000)
				}
				_ = json.NewEncoder(rw).Encode(map[string]any{
					"status": "success", "data": map[string]any{"resultType": "matrix", "result": lines},
				})
			}))
			defer fixture.Close()
			source, err := NewPrometheus(fixture.URL, "sample")
			if err != nil {
				t.Fatal(err)
			}
			got := source.Read(context.Background(), target, w)
			if got.Complete != (mode == "complete") {
				t.Fatalf("complete = %v for %s: %+v", got.Complete, mode, got)
			}
			switch mode {
			case "complete":
				if latency := got.Serving["latencyP95"]; latency.Value == nil || *latency.Value != 0.9 ||
					latency.Samples != 6 || latency.LastSample == nil || !latency.LastSample.Equal(w.End) {
					t.Fatalf("latency = %+v", latency)
				}
				checkValue(t, got.Serving["finishedRequests"], 20.0/30)
				checkValue(t, got.Serving["generatedTokens"], 10)
				checkValue(t, got.Serving["engineErrors"], 1.0/30)
				checkValue(t, got.GPUs["GPU-1"]["peakUsedVRAM"], 10240)
				checkValue(t, got.GPUs["GPU-1"]["minimumFreeVRAM"], 6000)
			case "wrong-node", "missing-gpu":
				want := map[string]string{"wrong-node": "label_mismatch", "missing-gpu": "missing"}[mode]
				if len(got.GPUs) != 0 || fmt.Sprint(got.Issues) != "[dcgm:"+want+"]" {
					t.Fatalf("GPU quality = %+v", got)
				}
			case "stale":
				if m := got.GPUs["GPU-1"]["meanUtilization"]; m.Quality != "stale" || m.Value != nil {
					t.Fatalf("stale utilization = %+v", m)
				}
			case "reset":
				if m := got.Serving["generatedTokens"]; m.Quality != "counter_reset" || m.Value != nil {
					t.Fatalf("reset counter = %+v", m)
				}
			}
		})
	}
}

func checkValue(t *testing.T, m Measurement, want float64) {
	t.Helper()
	if m.Value == nil || *m.Value != want || m.Quality != "measured" {
		t.Fatalf("metric = %+v, want %v", m, want)
	}
}
