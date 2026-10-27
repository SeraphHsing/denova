package statistics

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"denova/internal/app/projectbook"
	"denova/internal/project"
	stats "denova/internal/statistics"
)

func TestObservedSavedChangesDoNotInventRestartOrResyncOutput(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	content := filepath.Join(root, "projects", "Book")
	if err := os.MkdirAll(filepath.Join(content, "chapters"), 0700); err != nil {
		t.Fatal(err)
	}
	registry := project.NewRegistry(root)
	record, err := registry.Add(content, project.TypeBook, "Book")
	if err != nil {
		t.Fatal(err)
	}
	layout, err := registry.EnsureStore(record)
	if err != nil {
		t.Fatal(err)
	}
	books := projectbook.NewService(registry)
	service := New(registry, books)
	t.Cleanup(service.Close)
	save := func(words int) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(content, "chapters", "chapter1.md"), []byte(strings.Repeat("文", words)), 0600); err != nil {
			t.Fatal(err)
		}
		books.InvalidateSummary(record.ID, nil, true)
	}
	observe := func(mode observationMode) {
		t.Helper()
		if _, err := service.observe(ctx, record, layout, mode); err != nil {
			t.Fatal(err)
		}
	}
	save(100)
	observe(resyncBaseline)
	save(130)
	observe(snapshotOnly) // A dashboard read must not consume the pending change.
	observe(savedChange)
	save(80)
	observe(savedChange)
	save(150)
	observe(resyncBaseline) // Unobserved/relinked content is a new baseline.
	service.Close()
	service = New(registry, books)
	t.Cleanup(service.Close)
	save(180)
	observe(resyncBaseline) // Restart does not count the offline difference.
	save(190)
	observe(savedChange)
	report, err := service.Query(ctx, record.ID, 1, time.UTC, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Writing.Projects) != 1 || report.Writing.Projects[0].Words != 190 {
		t.Fatalf("current manuscript = %+v", report.Writing.Projects)
	}
	if delta := report.Writing.Days[0].Delta; delta == nil || *delta != -10 {
		t.Fatalf("observed delta = %v, want -10", delta)
	}
}

func TestProjectScopeAndViewerMidnight(t *testing.T) {
	root := t.TempDir()
	registry := project.NewRegistry(root)
	var records []project.Record
	for _, name := range []string{"A", "B"} {
		content := filepath.Join(root, "projects", name)
		if err := os.MkdirAll(content, 0700); err != nil {
			t.Fatal(err)
		}
		record, err := registry.Add(content, project.TypeGeneral, name)
		if err != nil {
			t.Fatal(err)
		}
		layout, err := registry.EnsureStore(record)
		if err != nil {
			t.Fatal(err)
		}
		records = append(records, record)
		for _, at := range []time.Time{time.Date(2026, 10, 25, 15, 59, 0, 0, time.UTC), time.Date(2026, 10, 25, 16, 1, 0, 0, time.UTC)} {
			if err := stats.AppendCall(layout.StoreRoot, stats.Call{ID: name, At: at, Kind: stats.ModelCall, Model: name, Runtime: "native", Usage: &stats.Usage{Total: 10}}); err != nil {
				t.Fatal(err)
			}
		}
	}
	service := New(registry, projectbook.NewService(registry))
	defer service.Close()
	now := time.Date(2026, 10, 26, 1, 0, 0, 0, time.UTC)
	report, err := service.Query(context.Background(), records[0].ID, 1, time.FixedZone("Asia/Shanghai", 480*60), now)
	if err != nil {
		t.Fatal(err)
	}
	if report.Usage.Totals.Requests != 1 || report.Usage.Totals.Tokens != 10 || report.Usage.Days[0].Date != "2026-10-26" || len(report.Usage.Models) != 1 {
		t.Fatalf("scoped midnight report = %+v", report.Usage)
	}
	report, err = service.Query(context.Background(), "", 1, time.FixedZone("Asia/Shanghai", 480*60), now)
	if err != nil {
		t.Fatal(err)
	}
	if report.Usage.Totals.Requests != 2 || report.Usage.Totals.Tokens != 20 {
		t.Fatalf("global usage = %+v", report.Usage.Totals)
	}
	// The fall-back day has 25 hours. Using its current UTC offset for the
	// whole date would incorrectly drop the first hour's observations.
	newYork, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	layout, err := registry.Layout(records[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, at := range []time.Time{
		time.Date(2026, 11, 1, 3, 59, 0, 0, time.UTC),
		time.Date(2026, 11, 1, 4, 1, 0, 0, time.UTC),
		time.Date(2026, 11, 2, 4, 59, 0, 0, time.UTC),
	} {
		if err := stats.AppendCall(layout.StoreRoot, stats.Call{ID: "dst", At: at, Kind: stats.ModelCall, Runtime: "native"}); err != nil {
			t.Fatal(err)
		}
	}
	report, err = service.Query(context.Background(), records[0].ID, 1, newYork, time.Date(2026, 11, 1, 17, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if report.Usage.Totals.Requests != 2 {
		t.Fatalf("daylight-saving usage = %+v", report.Usage.Totals)
	}
}

// Exercise percentile aggregation through persisted records and project/date
// filtering; an external turn must not contaminate Native latency samples.
func TestUsagePercentilesKeepRuntimeAndDateBoundaries(t *testing.T) {
	root := t.TempDir()
	content := filepath.Join(root, "projects", "Usage")
	if err := os.MkdirAll(content, 0700); err != nil {
		t.Fatal(err)
	}
	registry := project.NewRegistry(root)
	record, err := registry.Add(content, project.TypeGeneral, "Usage")
	if err != nil {
		t.Fatal(err)
	}
	layout, err := registry.EnsureStore(record)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	appendCall := func(call stats.Call) {
		t.Helper()
		if err := stats.AppendCall(layout.StoreRoot, call); err != nil {
			t.Fatal(err)
		}
	}
	for index := int64(1); index <= 20; index++ {
		appendCall(stats.Call{ID: "native", At: at, Kind: stats.ModelCall, Runtime: "native", Model: "model", DurationMS: index * 100, Failed: index == 20, Usage: &stats.Usage{Prompt: 10, Output: 2}})
	}
	appendCall(stats.Call{ID: "opaque", At: at, Kind: stats.RuntimeTurn, Runtime: "codex", Model: "model", DurationMS: 99999})
	appendCall(stats.Call{ID: "yesterday", At: at.Add(-24 * time.Hour), Kind: stats.ModelCall, Runtime: "native", Model: "model", DurationMS: 0})
	service := New(registry, projectbook.NewService(registry))
	defer service.Close()
	report, err := service.Query(context.Background(), record.ID, 1, time.UTC, at)
	if err != nil {
		t.Fatal(err)
	}
	check := func(summary UsageSummary, p50, p95 int64) {
		t.Helper()
		if summary.LatencyP50MS == nil || *summary.LatencyP50MS != p50 || summary.LatencyP95MS == nil || *summary.LatencyP95MS != p95 {
			t.Fatalf("latency summary = %+v, want %d/%d", summary, p50, p95)
		}
	}
	check(report.Usage.Totals, 1000, 1900)
	check(report.Usage.Days[0].UsageSummary, 1000, 1900)
	if report.Usage.Totals.Requests != 20 || report.Usage.Totals.ExternalTurns != 1 || report.Usage.Totals.UnknownUsage != 1 || report.Usage.Totals.FailedRequests != 1 || report.Usage.Totals.Tokens != 240 {
		t.Fatalf("usage = %+v", report.Usage.Totals)
	}
	if len(report.Usage.Models) != 2 {
		t.Fatalf("models = %+v", report.Usage.Models)
	}
	for _, model := range report.Usage.Models {
		if model.Runtime == "native" {
			check(model.UsageSummary, 1000, 1900)
		} else if model.LatencyP50MS != nil || model.LatencyP95MS != nil {
			t.Fatalf("opaque turn has latency samples: %+v", model)
		}
	}
	report, err = service.Query(context.Background(), record.ID, 1, time.UTC, at.Add(-24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	check(report.Usage.Totals, 0, 0)
	report, err = service.Query(context.Background(), record.ID, 1, time.UTC, at.Add(-48*time.Hour))
	if err != nil || report.Usage.Totals.LatencyP50MS != nil || report.Usage.Totals.LatencyP95MS != nil {
		t.Fatalf("empty period = %+v, error=%v", report.Usage.Totals, err)
	}
}
