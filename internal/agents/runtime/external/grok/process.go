package grok

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("Grok ACP error %d: %s", e.Code, e.Message) }
func (e *rpcError) dataCode() string {
	var data struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(e.Data, &data)
	return data.Code
}

type frame struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}
type process struct {
	ctx          context.Context
	client       *Client
	cmd          *exec.Cmd
	cancel       context.CancelFunc
	input        io.WriteCloser
	frames       chan frame
	done         chan struct{}
	readErr      error
	writeMu      sync.Mutex
	nextID       int
	session      string
	removeDir    string
	customModels bool
	configPath   string
}

func (c *Client) start(ctx context.Context, dir, model string) (_ *process, err error) {
	p := &process{client: c, frames: make(chan frame, 32), done: make(chan struct{})}
	if dir == "" {
		dir, err = os.MkdirTemp("", "denova-grok-")
		if err != nil {
			return nil, err
		}
		p.removeDir = dir
	}
	defer func() {
		if err != nil {
			_ = os.Remove(p.configPath)
			if p.removeDir != "" {
				_ = os.RemoveAll(p.removeDir)
			}
		}
	}()
	home := filepath.Join(dir, "grok-home")
	if err = os.MkdirAll(home, 0700); err != nil {
		return nil, err
	}
	// Keep credentials at their CLI-owned location while excluding personal MCP,
	// hooks and plugins. This private home is only a disposable runtime cache.
	config := "[cli]\nauto_update = false\n[session]\nload_envrc = false\n[managed_mcps]\nenabled = false\ngateway_tools_enabled = false\n[compat.claude]\nagents = false\nhooks = false\nmcps = false\nrules = false\nskills = false\n[compat.cursor]\nagents = false\nhooks = false\nmcps = false\nrules = false\nskills = false\n[compat.codex]\nhooks = false\nskills = false\n"
	// use_tool also accepts file-backed arguments. Native reads must never
	// bypass Host's scoped project tools, including that convenience path.
	config += "[permission]\ndeny = [\"Read(**)\"]\n"
	models, custom, err := modelConfiguration(c.env)
	if err != nil {
		return nil, fmt.Errorf("read Grok model configuration: %w", err)
	}
	p.customModels = custom
	config += "\n" + string(models)
	p.configPath = filepath.Join(home, "config.toml")
	if err = os.WriteFile(p.configPath, []byte(config), 0600); err != nil {
		return nil, err
	}
	runCtx, cancel := context.WithCancel(ctx)
	p.ctx = runCtx
	p.cancel = cancel
	args := []string{"agent", "--no-leader"}
	if model != "" && model != "default" {
		args = append(args, "--model", model)
	}
	args = append(args, "stdio")
	p.cmd = c.command(runCtx, args...)
	p.cmd.Dir = dir
	p.cmd.Env = isolatedEnvironment(p.cmd.Env, home)
	p.input, err = p.cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	reader, err := p.cmd.StdoutPipe()
	if err != nil {
		cancel()
		_ = p.input.Close()
		return nil, err
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		cancel()
		return nil, errors.New("Grok connection is closed")
	}
	err = p.cmd.Start()
	if err == nil {
		c.active[p] = struct{}{}
	}
	c.mu.Unlock()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("start Grok ACP: %w", err)
	}
	go func() {
		defer close(p.done)
		defer func() {
			if value := recover(); value != nil {
				p.readErr = errors.New("Grok ACP reader panicked")
				slog.ErrorContext(ctx, "Grok ACP reader panicked", "panic", value)
				cancel()
			}
		}()
		scanner := bufio.NewScanner(reader)
		scanner.Buffer(make([]byte, 64<<10), 32<<20)
		for scanner.Scan() {
			if len(strings.TrimSpace(scanner.Text())) == 0 {
				continue
			}
			var f frame
			if p.readErr = json.Unmarshal(scanner.Bytes(), &f); p.readErr != nil {
				return
			}
			select {
			case p.frames <- f:
			case <-runCtx.Done():
				return
			}
		}
		p.readErr = scanner.Err()
	}()
	startup, cancelStartup := context.WithTimeout(ctx, infrastructureTimeout)
	defer cancelStartup()
	var initialized struct {
		ProtocolVersion int                        `json:"protocolVersion"`
		Meta            map[string]json.RawMessage `json:"_meta"`
	}
	if err = p.call(startup, "initialize", map[string]any{"protocolVersion": 1, "clientInfo": map[string]string{"name": "denova", "version": "1"}, "clientCapabilities": map[string]any{"fs": map[string]bool{"readTextFile": false, "writeTextFile": false}, "terminal": false}}, &initialized, nil); err != nil {
		p.close()
		return nil, err
	}
	if initialized.ProtocolVersion != 1 {
		p.close()
		return nil, ErrVersionUnsupported
	}
	var sdk bool
	if json.Unmarshal(initialized.Meta["x.ai/mcp/sdk"], &sdk) != nil || !sdk {
		p.close()
		return nil, ErrVersionUnsupported
	}
	return p, nil
}

func isolatedEnvironment(base []string, home string) []string {
	authPath := environmentValue(base, "GROK_AUTH_PATH")
	if authPath == "" {
		original := originalHome(base)
		authPath = filepath.Join(original, "auth.json")
	}
	overrides := map[string]string{"GROK_HOME": home, "GROK_AUTH_PATH": authPath, "GROK_CONFIG": "", "GROK_CONFIG_PATH": "", "GROK_DISABLE_AUTOUPDATER": "1", "GROK_MANAGED_MCPS_ENABLED": "0", "GROK_MANAGED_MCP_GATEWAY_TOOLS_ENABLED": "0", "GROK_CLAUDE_MCPS_ENABLED": "0", "GROK_CURSOR_MCPS_ENABLED": "0", "GROK_CLAUDE_HOOKS_ENABLED": "0", "GROK_CURSOR_HOOKS_ENABLED": "0", "GROK_SUBAGENTS": "0"}
	out := make([]string, 0, len(base)+len(overrides))
	for _, entry := range base {
		key, _, _ := strings.Cut(entry, "=")
		if _, ok := overrides[strings.ToUpper(key)]; !ok {
			out = append(out, entry)
		}
	}
	for key, value := range overrides {
		out = append(out, key+"="+value)
	}
	return out
}

func (p *process) write(value any) error {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	return json.NewEncoder(p.input).Encode(value)
}

// One request loop also drains reverse MCP callbacks and ordered notifications.
// Callbacks finish durably before a terminal response can settle the attempt.
func (p *process) call(ctx context.Context, method string, params, output any, handle func(frame) error) error {
	p.nextID++
	id := p.nextID
	if err := p.write(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		return err
	}
	for {
		var f frame
		select {
		case <-ctx.Done():
			return ctx.Err()
		case f = <-p.frames:
		case <-p.done:
			select {
			case f = <-p.frames:
			default:
				return errors.Join(errors.New("Grok ACP stream closed before its response"), p.readErr)
			}
		}
		if f.Method != "" {
			if handle != nil {
				if err := handle(f); err != nil {
					return err
				}
			} else if len(f.ID) > 0 {
				return errors.New("unexpected Grok ACP callback during discovery")
			}
			continue
		}
		if string(f.ID) != fmt.Sprint(id) {
			return errors.New("Grok returned an unexpected response identity")
		}
		if f.Error != nil {
			return f.Error
		}
		if len(f.Result) == 0 {
			return errors.New("Grok omitted its ACP result")
		}
		if output == nil {
			return nil
		}
		return json.Unmarshal(f.Result, output)
	}
}

func (p *process) close() {
	if p.session != "" {
		_ = p.write(map[string]any{"jsonrpc": "2.0", "method": "session/cancel", "params": map[string]string{"sessionId": p.session}})
	}
	p.cancel()
	_ = p.input.Close()
	_ = p.cmd.Process.Kill()
	_ = p.cmd.Wait()
	<-p.done
	p.client.mu.Lock()
	delete(p.client.active, p)
	p.client.mu.Unlock()
	_ = os.Remove(p.configPath)
	if p.removeDir != "" {
		_ = os.RemoveAll(p.removeDir)
	}
}
