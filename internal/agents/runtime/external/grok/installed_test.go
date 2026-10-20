package grok

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Exercise the released executable against a local model fixture. This proves
// session creation and the CLI's real tool protocol without account credentials.
func TestInstalledGrokHostTools(t *testing.T) {
	executable := os.Getenv("DENOVA_TEST_GROK")
	if executable == "" {
		t.Skip("set DENOVA_TEST_GROK to exercise an installed Grok Build binary")
	}
	var mu sync.Mutex
	var modelRequests int
	fileInput := filepath.Join(t.TempDir(), "arguments.json")
	if err := os.WriteFile(fileInput, []byte(`{"text":"Forbidden native read"}`), 0600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		var request struct {
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			http.Error(w, "bad request", 400)
			return
		}
		auxiliary := len(request.Tools) == 0
		for _, tool := range request.Tools {
			if tool.Function.Name == "session_title" {
				auxiliary = true
			}
		}
		n := 0
		if !auxiliary {
			mu.Lock()
			modelRequests++
			n = modelRequests
			mu.Unlock()
		}
		for _, tool := range request.Tools {
			if !auxiliary && tool.Function.Name != "todo_write" && tool.Function.Name != "search_tool" && tool.Function.Name != "use_tool" && tool.Function.Name != "denova__save" {
				t.Errorf("unexpected native tool advertised: %s", tool.Function.Name)
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		write := func(value any) {
			if object, ok := value.(map[string]any); ok {
				object["created"] = 1
				object["model"] = "denova-test"
			}
			body, _ := json.Marshal(value)
			_, _ = fmt.Fprintf(w, "data: %s\n\n", body)
		}
		if auxiliary {
			write(map[string]any{"id": "title", "object": "chat.completion.chunk", "choices": []any{map[string]any{"index": 0, "delta": map[string]string{"role": "assistant", "content": "Story session"}, "finish_reason": "stop"}}})
		} else if n%2 == 1 {
			arguments := `{"tool_name":"denova__save","tool_input":{"text":"Story"}}`
			if n == 5 {
				body, _ := json.Marshal(map[string]string{"tool_name": "denova__save", "tool_input_file": fileInput})
				arguments = string(body)
			}
			write(map[string]any{"id": "completion-1", "object": "chat.completion.chunk", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"index": 0, "id": "tool-save", "type": "function", "function": map[string]string{"name": "use_tool", "arguments": arguments}}}}, "finish_reason": nil}}})
			write(map[string]any{"id": "completion-1", "object": "chat.completion.chunk", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "tool_calls"}}})
		} else {
			toolResult := false
			expected := "Saved"
			text := "Story saved."
			if n == 6 {
				expected = "Read denied"
				text = "Read rejected."
			}
			for _, message := range request.Messages {
				if message.Role == "tool" && strings.Contains(string(message.Content), expected) {
					toolResult = true
				}
			}
			if !toolResult {
				t.Errorf("host result was not returned to model on request %d", n)
			}
			write(map[string]any{"id": "completion-2", "object": "chat.completion.chunk", "choices": []any{map[string]any{"index": 0, "delta": map[string]string{"role": "assistant", "content": text}, "finish_reason": "stop"}}})
		}
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()
	userHome := t.TempDir()
	modelConfig := fmt.Sprintf("[model.denova-test]\nmodel = \"denova-test\"\nname = \"Denova Test\"\nbase_url = %q\napi_key = \"fixture-only\"\napi_backend = \"chat_completions\"\n[models]\ndefault = \"denova-test\"\n", server.URL+"/v1")
	if err := os.WriteFile(filepath.Join(userHome, "config.toml"), []byte(modelConfig), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client, err := Connect(ctx, ProcessOptions{Executable: executable})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	client.env = append(client.env, "GROK_HOME="+userHome, "GROK_AUTH_PATH="+filepath.Join(userHome, "auth.json"))
	input := fixtureInput(t)
	input.Selection.Grok.Model = "denova-test"
	input.Selection.Grok.Effort = ""
	host := &recordingHost{}
	result, err := client.Run(ctx, input, host)
	if err != nil {
		var rpc *rpcError
		if errors.As(err, &rpc) {
			t.Logf("Grok RPC failure: %s", rpc.Data)
		}
		t.Fatal(err)
	}
	if result.Text != "Story saved." || !result.Settled || len(host.calls) != 1 {
		t.Fatalf("real CLI tool result: %+v calls=%+v", result, host.calls)
	}
	input.SessionID = result.SessionID
	resumedHost := &recordingHost{}
	resumed, err := client.Run(ctx, input, resumedHost)
	if err != nil {
		t.Fatalf("real CLI resume: %v", err)
	}
	if resumed.Text != "Story saved." || !resumed.Settled || len(resumedHost.calls) != 1 {
		t.Fatalf("real CLI resumed result: %+v calls=%d", resumed, len(resumedHost.calls))
	}
	blockedHost := &recordingHost{}
	blocked, err := client.Run(ctx, input, blockedHost)
	if err != nil {
		t.Fatalf("real CLI native read check: %v", err)
	}
	if blocked.Text != "Read rejected." || !blocked.Settled || len(blockedHost.calls) != 0 {
		t.Fatalf("native file input bypassed Host isolation: %+v calls=%+v", blocked, blockedHost.calls)
	}
	t.Logf("Released Grok completed %d local model requests and two durable host calls across native session resume", modelRequests)
}
