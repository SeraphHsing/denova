package statistics

import (
	"math"
	"slices"

	stats "denova/internal/statistics"
)

// UsageSummary describes the selected interval. Latencies measure Native model
// calls, including unsuccessful calls; opaque runtime turns are not samples.
type UsageSummary struct {
	stats.Totals
	LatencyP50MS *int64 `json:"latency_p50_ms"`
	LatencyP95MS *int64 `json:"latency_p95_ms"`
}

type UsageDay struct {
	Date string `json:"date"`
	UsageSummary
}

type Model struct {
	Name    string `json:"name"`
	Runtime string `json:"runtime"`
	UsageSummary
}

// usageAccumulator derives percentiles from retained numeric observations. It
// never persists distributions or depends on optional diagnostic trace data.
type usageAccumulator struct {
	totals    stats.Totals
	durations []int64
}

func (usage *usageAccumulator) add(call stats.Call) {
	usage.totals.Add(call)
	if call.Kind == stats.ModelCall {
		usage.durations = append(usage.durations, call.DurationMS)
	}
}

func (usage *usageAccumulator) summary() UsageSummary {
	result := UsageSummary{Totals: usage.totals}
	if len(usage.durations) == 0 {
		return result
	}
	slices.Sort(usage.durations)
	// Nearest rank returns an observed duration, even for small sample counts.
	p50 := usage.durations[int(math.Ceil(float64(len(usage.durations))*0.50))-1]
	p95 := usage.durations[int(math.Ceil(float64(len(usage.durations))*0.95))-1]
	result.LatencyP50MS, result.LatencyP95MS = &p50, &p95
	return result
}
