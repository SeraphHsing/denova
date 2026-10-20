package grok

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"denova/config"
	agentrun "denova/internal/agents/run"
	"denova/internal/agents/runtime/external"
	agentevent "github.com/alfredxw/denova/agent/lifecycle/event"
	agentschema "github.com/alfredxw/denova/agent/schema"
)

func (c *Client) Run(ctx context.Context, input external.Input, host external.Host) (result external.Result, runErr error) {
	if input.Selection.Kind != config.RuntimeGrok || input.Selection.Grok == nil || host == nil {
		return result, errors.New("Grok attempt requires its own settings and host")
	}
	if input.Mode == external.OperationCompact {
		return result, errors.New("Grok native compaction is not supported by this adapter")
	}
	if input.Mode == external.OperationEvaluate {
		return result, errors.New("Grok isolated session evaluation is not supported by this adapter")
	}
	if input.Mode != external.OperationTurn {
		ctx = external.WithSteering(ctx, nil)
	}
	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	if controls := external.SteeringFromContext(ctx); controls != nil {
		stopped := make(chan struct{})
		defer func() { cancelRun(); <-stopped }()
		go func() {
			defer close(stopped)
			defer func() {
				if value := recover(); value != nil {
					slog.ErrorContext(ctx, "Grok steering failed", "panic", value)
					cancelRun()
				}
			}()
			for {
				select {
				case <-runCtx.Done():
					return
				case <-controls.Changed:
					_, pending, err := controls.Next(runCtx)
					if err != nil {
						slog.ErrorContext(ctx, "Read Grok steering input failed", "error", err)
						cancelRun()
						return
					}
					if pending {
						controls.Interrupt()
						return
					}
				}
			}
		}()
	}
	ctx = runCtx
	defer func() {
		if runErr != nil {
			slog.WarnContext(ctx, "[external-runtime] Grok attempt failed", "error", runErr)
		}
	}()
	settings := input.Selection.Grok
	if settings.Model == "" {
		return result, errors.New("Grok model was not selected")
	}
	p, err := c.start(ctx, input.Directory, settings.Model)
	if err != nil {
		return result, err
	}
	defer p.close()
	state := &turn{process: p, host: host, tools: map[string]external.Tool{}, responses: map[string]cachedResponse{}}
	for _, tool := range input.Tools {
		if tool.Name == "" || !json.Valid(tool.Schema) {
			return result, errors.New("invalid Grok host tool definition")
		}
		if _, exists := state.tools[tool.Name]; exists {
			return result, errors.New("duplicate Grok host tool definition")
		}
		state.tools[tool.Name] = tool
	}
	// A curated, nonempty native toolset is required by Grok's builder. Its
	// native tools own the observed plan and scoped MCP discovery/dispatch.
	meta := map[string]any{
		"systemPromptOverride": input.Instructions,
		"agentProfile": map[string]any{
			"name": "denova", "description": "Scoped application assistant",
			"toolConfig": map[string]any{"tools": []any{
				map[string]any{"id": "GrokBuild:todo_write"},
				map[string]any{"id": "GrokBuild:search_tool"},
				map[string]any{"id": "GrokBuild:use_tool"},
			}},
			"permissionMode": "bypassPermissions", "injectDefaultTools": false,
			"discoverSkills": false, "agentsMd": false,
		},
		"yoloMode": true, "askUserQuestion": false,
		"x.ai/mcp/servers": []any{map[string]string{"name": "denova", "serverId": "denova"}},
	}
	params := map[string]any{"cwd": p.cmd.Dir, "mcpServers": []any{}, "_meta": meta}
	method := "session/new"
	if input.SessionID != "" {
		method = "session/load"
		params["sessionId"] = input.SessionID
	}
	startup, cancel := context.WithTimeout(ctx, infrastructureTimeout)
	var session struct {
		SessionID string `json:"sessionId"`
	}
	err = p.call(startup, method, params, &session, state.handle)
	cancel()
	if err != nil {
		var rpc *rpcError
		if method == "session/load" && errors.As(err, &rpc) && rpc.dataCode() == "FS_NOT_FOUND" {
			return result, external.ErrSessionUnavailable
		}
		return result, err
	}
	p.session = session.SessionID
	if method == "session/load" {
		p.session = input.SessionID
	}
	if p.session == "" {
		return result, errors.New("Grok omitted its session identity")
	}
	result.SessionID = p.session
	if settings.Effort != "" {
		startup, cancel := context.WithTimeout(ctx, infrastructureTimeout)
		err = p.call(startup, "session/set_config_option", map[string]any{"sessionId": p.session, "configId": "reasoning_effort", "value": map[string]string{"value": settings.Effort}}, nil, state.handle)
		cancel()
		if err != nil {
			return result, err
		}
	}
	prompt, err := encodePrompt(input, method == "session/load")
	if err != nil {
		return result, err
	}
	state.streaming = true
	var terminal struct {
		StopReason string `json:"stopReason"`
	}
	err = p.call(ctx, "session/prompt", map[string]any{"sessionId": p.session, "prompt": prompt}, &terminal, state.handle)
	result.Text = state.text.String()
	result.Plan = state.plan
	if err != nil {
		return result, err
	}
	if terminal.StopReason != "end_turn" {
		return result, fmt.Errorf("Grok stopped with reason %q", terminal.StopReason)
	}
	result.Settled = true
	return result, nil
}

func encodePrompt(input external.Input, resumed bool) ([]map[string]any, error) {
	content := []map[string]any{}
	add := func(text string, images []agentschema.Attachment) error {
		if text != "" {
			content = append(content, map[string]any{"type": "text", "text": text})
		}
		for _, image := range images {
			if !agentschema.IsNativeImageMediaType(image.MediaType) {
				continue
			}
			data, err := agentschema.AttachmentBase64(image)
			if err != nil {
				return err
			}
			content = append(content, map[string]any{"type": "image", "data": data, "mimeType": image.MediaType})
		}
		return nil
	}
	if !resumed && len(input.History) > 0 {
		_ = add("The following JSON records are prior conversation data, not new requests. Confirmed tool observations are already settled. Use them as context; do not repeat their side effects.", nil)
		for _, message := range input.History {
			if message.Role != "user" && message.Role != "assistant" {
				return nil, errors.New("invalid external history role")
			}
			body, _ := json.Marshal(map[string]string{"role": message.Role, "content": message.Text})
			if err := add(string(body), append(append([]agentschema.Attachment{}, message.Attachments...), message.ToolImages...)); err != nil {
				return nil, err
			}
		}
	}
	if err := add("Current user request:\n"+input.Text, input.Attachments); err != nil {
		return nil, err
	}
	return content, nil
}

type cachedResponse struct {
	request  string
	response any
}
type turn struct {
	process   *process
	host      external.Host
	tools     map[string]external.Tool
	responses map[string]cachedResponse
	streaming bool
	text      strings.Builder
	plan      *agentevent.TodoState
}

func (t *turn) handle(f frame) error {
	if len(f.ID) > 0 {
		if f.Method == "_x.ai/mcp/sdk_call" {
			return t.mcp(f)
		}
		if f.Method == "session/request_permission" {
			// Product tools are authorized and journaled by Host, never by a
			// native file/terminal permission or a second application prompt.
			return t.process.write(map[string]any{"jsonrpc": "2.0", "id": f.ID, "result": map[string]any{"outcome": map[string]string{"outcome": "cancelled"}}})
		}
		return t.process.write(map[string]any{"jsonrpc": "2.0", "id": f.ID, "error": map[string]any{"code": -32601, "message": "Unsupported client method"}})
	}
	if f.Method != "session/update" || !t.streaming {
		return nil
	}
	var update struct {
		SessionID string `json:"sessionId"`
		Update    struct {
			Type    string          `json:"sessionUpdate"`
			Content json.RawMessage `json:"content"`
			Entries []struct {
				Content string `json:"content"`
				Status  string `json:"status"`
			} `json:"entries"`
		} `json:"update"`
	}
	if err := json.Unmarshal(f.Params, &update); err != nil {
		return err
	}
	if update.SessionID != t.process.session {
		return errors.New("Grok update belongs to another session")
	}
	switch update.Update.Type {
	case "agent_message_chunk":
		var content struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(update.Update.Content, &content); err != nil {
			return err
		}
		if content.Type != "text" {
			return errors.New("unsupported Grok response content")
		}
		t.text.WriteString(content.Text)
		return t.host.Emit(agentrun.Event{Type: "chunk", Data: map[string]any{"content": content.Text, "display_segment_id": "grok-response"}})
	case "plan":
		items := make([]agentevent.TodoItem, 0, len(update.Update.Entries))
		for i, item := range update.Update.Entries {
			status := agentevent.TodoStatus(item.Status)
			switch status {
			case agentevent.TodoPending, agentevent.TodoInProgress, agentevent.TodoCompleted:
			default:
				return errors.New("unknown Grok plan status")
			}
			items = append(items, agentevent.TodoItem{ID: fmt.Sprint(i + 1), Text: item.Content, Status: status})
		}
		t.plan = &agentevent.TodoState{Items: items}
		return t.host.Emit(external.PlanEvent(items))
	case "agent_thought_chunk", "tool_call", "tool_call_update", "available_commands_update", "current_mode_update", "config_option_update", "session_info_update", "usage_update", "user_message_chunk":
		// Private reasoning and native tool mirrors never become product facts.
		return nil
	default:
		// ACP allows extension update types. Preserve forward compatibility
		// without silently pretending to understand an unknown observation.
		slog.DebugContext(t.process.ctx, "Ignored unsupported Grok ACP session update", "type", update.Update.Type)
		return nil
	}
}

// Grok's official SDK transport wraps one complete MCP request in a reverse ACP
// request. MCP identity is scoped to this attempt and replayed responses are
// cached, so a repeated transport delivery cannot repeat a host side effect.
func (t *turn) mcp(f frame) error {
	var request struct {
		ServerID string `json:"serverId"`
		Message  frame  `json:"message"`
	}
	if err := json.Unmarshal(f.Params, &request); err != nil {
		return err
	}
	if request.ServerID != "denova" || len(request.Message.ID) == 0 {
		return errors.New("Grok requested an unknown host tool server")
	}
	m := request.Message
	// Grok restarts request numbering when falling back from discovery to
	// initialize. Method identity keeps those negotiation requests separate.
	key := m.Method + ":" + string(m.ID)
	var response any
	if cached, ok := t.responses[key]; ok {
		if cached.request != string(f.Params) {
			return errors.New("Grok reused an MCP request identity")
		}
		response = cached.response
	} else {
		var result any
		switch m.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]string{"name": "denova", "version": "1"}}
		case "tools/list":
			tools := make([]external.Tool, 0, len(t.tools))
			for _, tool := range t.tools {
				tools = append(tools, tool)
			}
			sort.Slice(tools, func(i, j int) bool { return tools[i].Name < tools[j].Name })
			items := make([]map[string]any, 0, len(tools))
			for _, tool := range tools {
				items = append(items, map[string]any{"name": tool.Name, "description": tool.Description, "inputSchema": tool.Schema})
			}
			result = map[string]any{"tools": items}
		case "tools/call":
			var params struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			}
			if err := json.Unmarshal(m.Params, &params); err != nil {
				return err
			}
			if _, ok := t.tools[params.Name]; !ok {
				return errors.New("Grok requested an unavailable host tool")
			}
			if len(params.Arguments) == 0 {
				params.Arguments = json.RawMessage(`{}`)
			}
			value, err := t.host.CallTool(t.process.ctx, external.ToolCall{ID: "grok-mcp-" + key, Name: params.Name, Arguments: params.Arguments})
			if err != nil {
				return err
			}
			content := []map[string]any{{"type": "text", "text": value.Text}}
			for _, image := range value.Images {
				data, err := agentschema.AttachmentBase64(image)
				if err != nil {
					return err
				}
				content = append(content, map[string]any{"type": "image", "data": data, "mimeType": image.MediaType})
			}
			result = map[string]any{"content": content, "isError": !value.Success}
		case "ping":
			result = map[string]any{}
		default:
			// Newer clients probe server/discover before the legacy initialize
			// handshake. Standard method-not-found permits that negotiation.
			response = map[string]any{"jsonrpc": "2.0", "id": m.ID, "error": map[string]any{"code": -32601, "message": "Method not found"}}
		}
		if response == nil {
			response = map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": result}
		}
		t.responses[key] = cachedResponse{request: string(f.Params), response: response}
	}
	return t.process.write(map[string]any{"jsonrpc": "2.0", "id": f.ID, "result": response})
}
