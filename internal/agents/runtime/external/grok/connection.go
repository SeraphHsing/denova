// Package grok drives Grok Build's native ACP and MCP-over-ACP protocols.
// The CLI's transcript is a disposable cache; product tools execute on Host.
package grok

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"denova/internal/agents/runtime/external"
	"denova/internal/hostruntime"
	"github.com/Masterminds/semver/v3"
)

const infrastructureTimeout = 15 * time.Second

// MinimumVersion is the released version exercised by native ACP/tool tests.
const MinimumVersion = "1.0.62"

var ErrVersionUnsupported = errors.New("unsupported Grok Build version or ACP capabilities")

// ProcessOptions contains only host-local executable discovery, never journal data.
type ProcessOptions struct {
	Executable string
	Args       []string
}

type Client struct {
	options ProcessOptions
	env     []string
	version string
	mu      sync.Mutex
	closed  bool
	state   external.ConnectionState
	active  map[*process]struct{}
}

func Connect(ctx context.Context, options ProcessOptions) (*Client, error) {
	if !filepath.IsAbs(options.Executable) {
		return nil, errors.New("Grok executable must be absolute")
	}
	c := &Client{options: options, env: hostruntime.WithSystemProxy(ctx, os.Environ()), active: map[*process]struct{}{}, state: external.ConnectionState{Status: "unchecked"}}
	ctx, cancel := context.WithTimeout(ctx, infrastructureTimeout)
	defer cancel()
	body, err := c.command(ctx, "--version").Output()
	if err != nil {
		return nil, fmt.Errorf("read Grok Build version: %w", err)
	}
	fields := strings.Fields(string(body))
	if len(fields) < 2 || fields[0] != "grok" {
		return nil, ErrVersionUnsupported
	}
	version, err := semver.StrictNewVersion(fields[1])
	if err != nil || version.LessThan(semver.MustParse(MinimumVersion)) {
		return nil, ErrVersionUnsupported
	}
	c.version = version.String()
	return c, nil
}

func (c *Client) command(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, c.options.Executable, append(append([]string{}, c.options.Args...), args...)...)
	cmd.Env, cmd.Stderr, cmd.WaitDelay = append([]string{}, c.env...), io.Discard, infrastructureTimeout
	configureProcess(cmd)
	return cmd
}

func (c *Client) Version() string                  { return "acp-v1/" + c.version }
func (c *Client) Status() external.ConnectionState { c.mu.Lock(); defer c.mu.Unlock(); return c.state }
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	c.state = external.ConnectionState{Status: "unavailable", ReasonKey: "agentRuntime.connectionLost"}
	for p := range c.active {
		p.cancel()
	}
	return nil
}

func (c *Client) Check(ctx context.Context) (state external.ConnectionState, err error) {
	state = external.ConnectionState{Status: "unavailable", ReasonKey: "agentRuntime.connectionFailed"}
	defer func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.closed {
			return
		}
		// A transient ACP discovery failure must not discard a runtime that was
		// usable a moment ago. The explicit check still reports the error.
		if state.Status == "unavailable" && c.state.Status == "ready" {
			return
		}
		c.state = state
	}()
	ctx, cancel := context.WithTimeout(ctx, infrastructureTimeout)
	defer cancel()
	p, err := c.start(ctx, "", "")
	if err != nil {
		return state, err
	}
	defer p.close()
	var auth struct {
		MethodID *string `json:"methodId"`
	}
	if err := p.call(ctx, "_x.ai/auth/info", map[string]any{}, &auth, nil); err != nil {
		return state, err
	}
	if (auth.MethodID == nil || *auth.MethodID == "") && !p.customModels {
		return external.ConnectionState{Status: "auth_required"}, nil
	}
	return external.ConnectionState{Status: "ready"}, nil
}

func (c *Client) Models(ctx context.Context) (external.Models, error) {
	ctx, cancel := context.WithTimeout(ctx, infrastructureTimeout)
	defer cancel()
	p, err := c.start(ctx, "", "")
	if err != nil {
		return external.Models{}, err
	}
	defer p.close()
	var envelope struct {
		Result *modelState     `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	if err := p.call(ctx, "_x.ai/models/list", map[string]any{}, &envelope, nil); err != nil {
		return external.Models{}, err
	}
	if envelope.Result == nil || (len(envelope.Error) > 0 && string(envelope.Error) != "null") {
		return external.Models{}, errors.New("Grok model discovery failed")
	}
	return envelope.Result.catalog()
}

type modelState struct {
	Current   string `json:"currentModelId"`
	Available []struct {
		ID   string `json:"modelId"`
		Name string `json:"name"`
		Meta struct {
			Effort  string `json:"reasoningEffort"`
			Efforts []struct {
				Value string `json:"value"`
			} `json:"reasoningEfforts"`
		} `json:"_meta"`
	} `json:"availableModels"`
}

func (s modelState) catalog() (external.Models, error) {
	models := external.Models{DefaultID: s.Current, Items: []external.Model{}}
	seen := map[string]bool{}
	for _, m := range s.Available {
		if m.ID == "" || m.Name == "" || seen[m.ID] {
			return models, errors.New("Grok returned an invalid model catalog")
		}
		seen[m.ID] = true
		item := external.Model{ID: m.ID, DisplayName: m.Name, DefaultEffort: m.Meta.Effort, Efforts: []string{}}
		for _, effort := range m.Meta.Efforts {
			if effort.Value != "" {
				item.Efforts = append(item.Efforts, effort.Value)
			}
		}
		models.Items = append(models.Items, item)
	}
	if len(models.Items) == 0 || !seen[models.DefaultID] {
		return models, errors.New("Grok omitted its available/default models")
	}
	return models, nil
}
