package observation

import (
	"encoding/json"
	"math"
	"slices"
	"strconv"
	"time"
)

const maxScrapeGap = 45 * time.Second

type summary struct {
	Measurement
	delta float64
	mean  float64
	min   float64
	max   float64
}

func summarize(lines []series, w Window, source, unit string, limit float64, counter bool) summary {
	m := summary{Measurement: Measurement{Source: source, Unit: unit, Quality: "missing"}}
	if len(lines) == 0 || len(lines) == 1 && len(lines[0].Values) == 0 {
		return m
	}
	if len(lines) > 1 {
		m.Quality = "ambiguous_series"
		return m
	}
	points := lines[0].Values
	m.min = math.Inf(1)
	var previousAt time.Time
	var previous float64
	for i, point := range points {
		var seconds float64
		var raw string
		if json.Unmarshal(point[0], &seconds) != nil || json.Unmarshal(point[1], &raw) != nil {
			m.Quality = "invalid_sample"
			return m
		}
		at := time.UnixMilli(int64(math.Round(seconds * 1000))).UTC()
		value, err := strconv.ParseFloat(raw, 64)
		if err != nil || !finite(value) || value < 0 || value > limit || at.Before(w.Start) || at.After(w.End) {
			m.Quality = "invalid_sample"
			return m
		}
		if i == 0 {
			m.FirstSample = &at
		} else {
			if !at.After(previousAt) {
				m.Quality = "invalid_sample"
				return m
			}
			if at.Sub(previousAt) > maxScrapeGap {
				m.Quality = "scrape_gap"
				return m
			}
			if counter && value < previous {
				m.Quality = "counter_reset"
				return m
			}
			m.delta += value - previous
		}
		m.LastSample = &at
		m.Samples++
		m.min, m.max = math.Min(m.min, value), math.Max(m.max, value)
		m.mean += (value - m.mean) / float64(i+1)
		previousAt, previous = at, value
	}
	switch {
	case w.End.Sub(*m.LastSample) > maxScrapeGap:
		m.Quality = "stale"
	case m.Samples < 2 || m.FirstSample.Sub(w.Start) > maxScrapeGap:
		m.Quality = "incomplete_window"
	default:
		m.Quality = "measured"
	}
	return m
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

func (s summary) value(v float64) Measurement {
	if s.Quality == "measured" {
		if finite(v) {
			s.Value = &v
		} else {
			s.Quality = "invalid_sample"
		}
	}
	return s.Measurement
}

func (s summary) rate() Measurement {
	if s.Quality != "measured" {
		return s.Measurement
	}
	return s.value(s.delta / s.LastSample.Sub(*s.FirstSample).Seconds())
}

func summarizeServing(lines []series, w Window, sourceIssue string) map[string]Measurement {
	const source = "prometheus/vllm"
	byName := map[string][]series{}
	for _, line := range lines {
		byName[line.Labels["__name__"]] = append(byName[line.Labels["__name__"]], line)
	}
	count := summarize(withLabel(byName[latencyBuckets], "le", "+Inf"), w, source, "requests/s", math.MaxFloat64, true)
	tokens := summarize(byName[generation], w, source, "tokens/s", math.MaxFloat64, true)
	queue := summarize(byName[waiting], w, source, "requests", math.MaxFloat64, false)
	failed := summarize(withLabel(byName[finished], "finished_reason", "error"), w, source, "requests/s", math.MaxFloat64, true)
	aborted := summarize(withLabel(byName[finished], "finished_reason", "abort"), w, source, "requests/s", math.MaxFloat64, true)
	out := map[string]Measurement{
		"latencyP95":       p95(byName[latencyBuckets], w, count),
		"finishedRequests": count.rate(),
		"generatedTokens":  tokens.rate(),
		"waitingRequests":  queue.value(queue.mean),
		"engineErrors":     failed.rate(),
		"clientAborts":     aborted.rate(),
	}
	if sourceIssue != "" {
		for key, m := range out {
			m.Quality = sourceIssue
			out[key] = m
		}
	}
	return out
}

func withLabel(lines []series, key, value string) []series {
	var matching []series
	for _, line := range lines {
		if line.Labels[key] == value {
			matching = append(matching, line)
		}
	}
	return matching
}

func p95(lines []series, w Window, count summary) Measurement {
	count.Unit = "s"
	if count.Quality != "measured" {
		return count.Measurement
	}
	if count.delta == 0 {
		count.Quality = "no_requests"
		return count.Measurement
	}
	type bucket struct{ bound, count float64 }
	buckets := make([]bucket, 0, len(lines))
	inf := withLabel(lines, "le", "+Inf")[0].Labels
	for _, line := range lines {
		if line.Labels["le"] == "+Inf" {
			continue
		}
		bound, err := strconv.ParseFloat(line.Labels["le"], 64)
		s := summarize([]series{line}, w, count.Source, count.Unit, math.MaxFloat64, true)
		if err != nil || !finite(bound) || bound <= 0 || s.Quality != "measured" ||
			s.Samples != count.Samples || !s.FirstSample.Equal(*count.FirstSample) || !s.LastSample.Equal(*count.LastSample) ||
			line.Labels["model_name"] != inf["model_name"] || line.Labels["engine"] != inf["engine"] ||
			line.Labels["instance"] != inf["instance"] {
			count.Quality = "invalid_histogram"
			return count.Measurement
		}
		buckets = append(buckets, bucket{bound, s.delta})
	}
	slices.SortFunc(buckets, func(a, b bucket) int {
		switch {
		case a.bound < b.bound:
			return -1
		case a.bound > b.bound:
			return 1
		default:
			return 0
		}
	})
	var prev bucket
	for _, b := range buckets {
		if b.bound <= prev.bound || b.count < prev.count || b.count > count.delta {
			count.Quality = "invalid_histogram"
			return count.Measurement
		}
		prev = b
	}
	prev = bucket{}
	rank := 0.95 * count.delta
	for _, b := range buckets {
		if b.count >= rank {
			return count.value(prev.bound + (b.bound-prev.bound)*(rank-prev.count)/(b.count-prev.count))
		}
		prev = b
	}
	count.Quality = "above_last_bucket"
	return count.Measurement
}

func summarizeGPUs(lines []series, w Window) map[string]map[string]Measurement {
	byGPU := map[string]map[string][]series{}
	for _, line := range lines {
		id := line.Labels["UUID"]
		if id == "" {
			continue
		}
		if byGPU[id] == nil {
			byGPU[id] = map[string][]series{}
		}
		name := line.Labels["__name__"]
		byGPU[id][name] = append(byGPU[id][name], line)
	}
	out := map[string]map[string]Measurement{}
	for id, device := range byGPU {
		util := summarize(device[gpuUtil], w, "prometheus/dcgm", "%", 100, false)
		out[id] = map[string]Measurement{
			"meanUtilization": util.value(util.mean),
			"peakUsedVRAM":    gpuMemory(device, gpuUsed, w, true),
			"minimumFreeVRAM": gpuMemory(device, gpuFree, w, false),
		}
	}
	return out
}

func gpuMemory(device map[string][]series, name string, w Window, peak bool) Measurement {
	lines := device[name]
	scale := float64(1)
	if bytes := device[name+"_BYTES"]; len(bytes) != 0 {
		lines = bytes
		scale = 1 << 20
	}
	// DCGM exposes sentinel values far above any supported framebuffer size.
	s := summarize(lines, w, "prometheus/dcgm", "MiB", (1<<24)*scale, false)
	if peak {
		return s.value(s.max / scale)
	}
	return s.value(s.min / scale)
}

func allMeasured(values map[string]Measurement) bool {
	for _, m := range values {
		if m.Quality != "measured" {
			return false
		}
	}
	return true
}
