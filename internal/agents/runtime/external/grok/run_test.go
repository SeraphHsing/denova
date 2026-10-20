package grok

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"denova/config"
	agentrun "denova/internal/agents/run"
	"denova/internal/agents/runtime/external"
)

func fixtureClient(t *testing.T, mode string) *Client {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	c := &Client{options: ProcessOptions{Executable: executable, Args: []string{"-test.run=^TestGrokACPProcess$", "--"}}, env: append(os.Environ(), "DENOVA_GROK_HELPER=1", "DENOVA_GROK_MODE="+mode), active: map[*process]struct{}{}}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

type recordingHost struct {
	mu      sync.Mutex
	calls   []external.ToolCall
	events  []agentrun.Event
	started chan struct{}
	wait    bool
}

func (h *recordingHost) Emit(event agentrun.Event) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.events = append(h.events, event)
	return nil
}
func (h *recordingHost) CallTool(ctx context.Context, call external.ToolCall) (external.ToolResult, error) {
	h.mu.Lock()
	h.calls = append(h.calls, call)
	h.mu.Unlock()
	if h.started != nil {
		close(h.started)
	}
	if h.wait {
		<-ctx.Done()
		return external.ToolResult{}, ctx.Err()
	}
	return external.ToolResult{Text: "Saved", Success: true}, nil
}
func fixtureInput(t *testing.T) external.Input {
	return external.Input{Selection: config.RuntimeSelection{Kind: config.RuntimeGrok, Grok: &config.GrokRuntimeSettings{Model: "grok-test", Effort: "high"}}, Directory: t.TempDir(), Instructions: "Write the requested story.", Text: "Save the story", Tools: []external.Tool{{Name: "save", Description: "Save a story", Schema: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}}}`)}}}
}

func TestRunRoutesReverseMCPAndSettlesAfterHost(t *testing.T) {
	c := fixtureClient(t, "turn")
	host := &recordingHost{}
	result, err := c.Run(context.Background(), fixtureInput(t), host)
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "Story saved." || result.SessionID != "session-1" || !result.Settled || result.Plan == nil || len(result.Plan.Items) != 1 || string(result.Plan.Items[0].Status) != "completed" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if len(host.calls) != 1 || host.calls[0].Name != "save" || string(host.calls[0].Arguments) != `{"text":"Story"}` {
		t.Fatalf("replayed or incorrect tool call: %+v", host.calls)
	}
	for _, event := range host.events {
		if strings.Contains(fmt.Sprint(event.Data), "PRIVATE") {
			t.Fatal("private reasoning reached the product journal")
		}
	}
}

func TestModelsUseNativeCatalogAndEfforts(t *testing.T) {
	models, err := fixtureClient(t, "models").Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models.Items) != 1 || models.DefaultID != "grok-test" || models.Items[0].DisplayName != "Grok Test" || len(models.Items[0].Efforts) != 2 || models.Items[0].DefaultEffort != "high" {
		t.Fatalf("unexpected catalog: %+v", models)
	}
}

func TestResumeSkipsReplayAndMissingCacheIsRecoverable(t *testing.T) {
	input := fixtureInput(t)
	input.SessionID = "session-1"
	input.History = []external.Message{{Role: "user", Text: "old request"}}
	result, err := fixtureClient(t, "resume").Run(context.Background(), input, &recordingHost{})
	if err != nil || result.Text != "Story saved." {
		t.Fatalf("resume: %+v %v", result, err)
	}
	_, err = fixtureClient(t, "missing").Run(context.Background(), input, &recordingHost{})
	if !errors.Is(err, external.ErrSessionUnavailable) {
		t.Fatalf("missing cache was not recoverable: %v", err)
	}
}

func TestCancellationReachesHostAndDoesNotSettle(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	host := &recordingHost{started: make(chan struct{}), wait: true}
	done := make(chan error, 1)
	go func() {
		defer func() {
			if value := recover(); value != nil {
				done <- fmt.Errorf("run panicked: %v", value)
			}
		}()
		result, err := fixtureClient(t, "turn").Run(ctx, fixtureInput(t), host)
		if result.Settled {
			err = errors.New("cancelled attempt settled")
		}
		done <- err
	}()
	select {
	case <-host.started:
		cancel()
	case <-time.After(2 * time.Second):
		t.Fatal("host tool did not start")
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancel did not reach the host")
	}
}

func TestProtocolFailuresCannotSettle(t *testing.T) {
	for _, mode := range []string{"no-sdk", "bad-tool", "bad-stop"} {
		t.Run(mode, func(t *testing.T) {
			result, err := fixtureClient(t, mode).Run(context.Background(), fixtureInput(t), &recordingHost{})
			if err == nil || result.Settled {
				t.Fatalf("accepted invalid protocol result: %+v %v", result, err)
			}
			if mode == "no-sdk" && !errors.Is(err, ErrVersionUnsupported) {
				t.Fatalf("missing protocol capability was not classified: %v", err)
			}
		})
	}
}

func TestSteeringInterruptsWithoutConsumingQueuedInput(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	changed := make(chan struct{}, 1)
	interrupted := make(chan struct{}, 1)
	ctx = external.WithSteering(ctx, &external.Steering{
		Changed:   changed,
		Next:      func(context.Context) (external.Guidance, bool, error) { return external.Guidance{}, true, nil },
		Interrupt: func() { interrupted <- struct{}{}; cancel() },
	})
	client := fixtureClient(t, "turn")
	input := fixtureInput(t)
	host := &recordingHost{started: make(chan struct{}), wait: true}
	done := make(chan error, 1)
	go func() {
		defer func() {
			if value := recover(); value != nil {
				done <- fmt.Errorf("steering run panicked: %v", value)
			}
		}()
		_, err := client.Run(ctx, input, host)
		done <- err
	}()
	select {
	case <-host.started:
		changed <- struct{}{}
	case <-time.After(2 * time.Second):
		t.Fatal("tool did not start")
	}
	select {
	case <-interrupted:
	case <-time.After(time.Second):
		t.Fatal("steering did not interrupt")
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("steering failed: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("interrupted attempt did not stop")
	}
}

// The helper is a subprocess protocol fixture, not an alternative adapter.
func TestGrokACPProcess(t *testing.T) {
	if os.Getenv("DENOVA_GROK_HELPER") != "1" {
		return
	}
	mode := os.Getenv("DENOVA_GROK_MODE")
	enc := json.NewEncoder(os.Stdout)
	write := func(value any) {
		if err := enc.Encode(value); err != nil {
			os.Exit(2)
		}
	}
	response := func(id json.RawMessage, result any) {
		write(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	}
	reverse := func(outer string, inner int, method string, params any) {
		write(map[string]any{"jsonrpc": "2.0", "id": outer, "method": "_x.ai/mcp/sdk_call", "params": map[string]any{"serverId": "denova", "message": map[string]any{"jsonrpc": "2.0", "id": inner, "method": method, "params": params}}})
	}
	update := func(kind string, fields map[string]any) {
		fields["sessionUpdate"] = kind
		write(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": "session-1", "update": fields}})
	}
	var sessionID, promptID json.RawMessage
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var f frame
		if json.Unmarshal(scanner.Bytes(), &f) != nil {
			os.Exit(3)
		}
		switch f.Method {
		case "initialize":
			response(f.ID, map[string]any{"protocolVersion": 1, "_meta": map[string]any{"x.ai/mcp/sdk": mode != "no-sdk"}})
		case "_x.ai/auth/info":
			response(f.ID, map[string]string{"methodId": "xai_api_key"})
		case "_x.ai/models/list":
			response(f.ID, map[string]any{"result": map[string]any{"currentModelId": "grok-test", "availableModels": []any{map[string]any{"modelId": "grok-test", "name": "Grok Test", "_meta": map[string]any{"reasoningEffort": "high", "reasoningEfforts": []any{map[string]string{"value": "low"}, map[string]string{"value": "high"}}}}}}})
		case "session/new", "session/load":
			if mode == "missing" {
				write(map[string]any{"jsonrpc": "2.0", "id": f.ID, "error": map[string]any{"code": -32603, "message": "Path not found.", "data": map[string]string{"code": "FS_NOT_FOUND"}}})
				continue
			}
			var params map[string]any
			_ = json.Unmarshal(f.Params, &params)
			meta, _ := params["_meta"].(map[string]any)
			profile, _ := meta["agentProfile"].(map[string]any)
			if profile["injectDefaultTools"] != false || profile["agentsMd"] != false || len(params["mcpServers"].([]any)) != 0 {
				os.Exit(4)
			}
			if f.Method == "session/load" {
				update("agent_message_chunk", map[string]any{"content": map[string]string{"type": "text", "text": "OLD REPLAY"}})
			}
			sessionID = f.ID
			reverse("mcp-discover", 0, "server/discover", map[string]any{})
		case "session/set_config_option":
			response(f.ID, map[string]any{"configOptions": []any{}})
		case "session/prompt":
			if mode == "resume" && strings.Contains(string(f.Params), "old request") {
				os.Exit(5)
			}
			promptID = f.ID
			update("tool_call", map[string]any{"content": []any{}, "toolCallId": "tool-save", "title": "use_tool", "status": "in_progress"})
			update("agent_thought_chunk", map[string]any{"content": map[string]string{"type": "text", "text": "PRIVATE"}})
			tool := "save"
			if mode == "bad-tool" {
				tool = "unavailable"
			}
			reverse("mcp-call", 3, "tools/call", map[string]any{"name": tool, "arguments": map[string]string{"text": "Story"}})
		case "session/cancel":
			os.Exit(0)
		case "":
			switch string(f.ID) {
			case `"mcp-discover"`:
				var reply frame
				_ = json.Unmarshal(f.Result, &reply)
				if reply.Error == nil || reply.Error.Code != -32601 {
					os.Exit(6)
				}
				reverse("mcp-init", 0, "initialize", map[string]string{"protocolVersion": "2024-11-05"})
			case `"mcp-init"`:
				reverse("mcp-list", 2, "tools/list", map[string]any{})
			case `"mcp-list"`:
				response(sessionID, map[string]string{"sessionId": "session-1"})
			case `"mcp-call"`:
				reverse("mcp-replay", 3, "tools/call", map[string]any{"name": "save", "arguments": map[string]string{"text": "Story"}})
			case `"mcp-replay"`:
				update("plan", map[string]any{"entries": []any{map[string]string{"content": "Save story", "status": "completed", "priority": "medium"}}})
				update("agent_message_chunk", map[string]any{"content": map[string]string{"type": "text", "text": "Story saved."}})
				stop := "end_turn"
				if mode == "bad-stop" {
					stop = "max_tokens"
				}
				response(promptID, map[string]string{"stopReason": stop})
			}
		}
	}
	os.Exit(0)
}

func TestInstalledGrokACP(t *testing.T) {
	executable := os.Getenv("DENOVA_TEST_GROK")
	if executable == "" {
		t.Skip("set DENOVA_TEST_GROK to exercise an installed Grok Build binary")
	}
	ctx, cancel := context.WithTimeout(context.Background(), infrastructureTimeout)
	defer cancel()
	client, err := Connect(ctx, ProcessOptions{Executable: executable})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	models, err := client.Models(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(models.Items) == 0 {
		t.Fatal("empty real Grok catalog")
	}
	t.Logf("Grok protocol %s returned %d models", client.Version(), len(models.Items))
	state, err := client.Check(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != "ready" && state.Status != "auth_required" {
		t.Fatalf("unexpected authentication state: %+v", state)
	}
	_, err = exec.LookPath(executable)
	if err != nil {
		t.Fatal(err)
	}
}
