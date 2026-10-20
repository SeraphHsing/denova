package agentruntime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"sync"
	"time"

	"denova/config"
	"denova/internal/agents/conversationconfig"
	"denova/internal/agents/runtime/external"
	"denova/internal/agents/runtime/external/antigravity"
	"denova/internal/agents/runtime/external/claude"
	"denova/internal/agents/runtime/external/codex"
	"denova/internal/agents/runtime/external/grok"
	"denova/internal/hostruntime"
)

var (
	ErrEngineNotFound         = errors.New("agent runtime is not registered")
	ErrEngineNotInstalled     = errors.New("agent runtime is not installed")
	ErrEngineNotReady         = errors.New("agent runtime is not ready")
	ErrEngineModelUnavailable = errors.New("agent runtime model or effort is unavailable")
)

// VersionUnsupportedReasonKey maps adapter version errors to localized product guidance.
// API handlers must not import a provider implementation to classify failures.
func VersionUnsupportedReasonKey(err error) string {
	switch {
	case errors.Is(err, codex.ErrVersionUnsupported):
		return "agentRuntime.incompatibleVersion"
	case errors.Is(err, claude.ErrVersionUnsupported):
		return "agentRuntime.claudeIncompatibleVersion"
	case errors.Is(err, antigravity.ErrVersionUnsupported):
		return "agentRuntime.antigravityIncompatibleVersion"
	case errors.Is(err, grok.ErrVersionUnsupported):
		return "agentRuntime.grokIncompatibleVersion"
	default:
		return ""
	}
}

// Engines owns host-local connections and their active-use leases. Creating or
// listing it has no filesystem, process, account, or network side effects.
// Native execution never calls Acquire and retains its existing runtime.
type Engines struct {
	controlsMu sync.Mutex
	controls   map[string]*ExternalController
	// Admissions may prepare concurrently. A selection change excludes their
	// final accept step across Writing and AgentChat, which share one journal.
	admission      sync.RWMutex
	entries        map[config.RuntimeID]*engineEntry
	Operations     external.Service
	apiMu          sync.Mutex
	apiConnections map[external.Connection]struct{}
	apiClosed      bool
	apiFactory     func(context.Context, config.RuntimeID, config.ResolvedModelSettings) (external.Connection, error)
}

type engineEntry struct {
	mu           sync.Mutex
	id           config.RuntimeID
	factory      func(context.Context) (external.Connection, error)
	connection   external.Connection
	state        external.ConnectionState
	models       external.Models
	modelsExpiry time.Time
	users        int
	closed       bool
}

// engineModelsTTL bounds repeated CLI model discovery while the runtime menu
// reopens. An explicit Check invalidates it because login and CLI updates can
// change the catalog.
const engineModelsTTL = 5 * time.Minute

func NewEngines() *Engines {
	return &Engines{apiConnections: make(map[external.Connection]struct{}), apiFactory: connectRuntimeAPI, entries: map[config.RuntimeID]*engineEntry{config.RuntimeCodex: {
		id: config.RuntimeCodex, state: external.ConnectionState{Status: "unchecked"}, factory: connectCodex,
	}, config.RuntimeClaude: {id: config.RuntimeClaude, state: external.ConnectionState{Status: "unchecked"}, factory: connectClaude},
		config.RuntimeGrok:        {id: config.RuntimeGrok, state: external.ConnectionState{Status: "unchecked"}, factory: connectGrok},
		config.RuntimeAntigravity: {id: config.RuntimeAntigravity, state: external.ConnectionState{Status: "unchecked"}, factory: connectAntigravity}}}
}

func connectGrok(ctx context.Context) (external.Connection, error) {
	executable := hostruntime.DiscoverGrok(os.Environ())
	if executable == "" {
		return nil, ErrEngineNotInstalled
	}
	return grok.Connect(ctx, grok.ProcessOptions{Executable: executable})
}

func connectAntigravity(ctx context.Context) (external.Connection, error) {
	executable := hostruntime.DiscoverAntigravity(os.Environ())
	if executable == "" {
		return nil, ErrEngineNotInstalled
	}
	return antigravity.Connect(ctx, antigravity.ProcessOptions{Executable: executable})
}

func connectClaude(ctx context.Context) (external.Connection, error) {
	launch := hostruntime.DiscoverClaude(os.Environ())
	if launch.Executable == "" {
		return nil, ErrEngineNotInstalled
	}
	return claude.Connect(ctx, claude.ProcessOptions{Launch: launch})
}

func connectCodex(ctx context.Context) (external.Connection, error) {
	executable := hostruntime.DiscoverCodex(os.Environ())
	if executable == "" {
		return nil, ErrEngineNotInstalled
	}
	home, err := hostruntime.CodexHome(os.Environ())
	if err != nil {
		return nil, err
	}
	return codex.Connect(ctx, codex.ProcessOptions{Executable: executable, Home: home})
}

func (engines *Engines) Catalog() []EngineDescriptor {
	items := []EngineDescriptor{engineDescriptor(config.RuntimeNative)}
	for _, id := range []config.RuntimeID{config.RuntimeCodex, config.RuntimeClaude, config.RuntimeGrok, config.RuntimeAntigravity} {
		entry := engines.entries[id]
		entry.mu.Lock()
		items = append(items, entry.descriptor())
		entry.mu.Unlock()
	}
	return items
}

func (entry *engineEntry) descriptor() EngineDescriptor {
	item := engineDescriptor(entry.id)
	state := entry.state
	if entry.connection != nil {
		state = entry.connection.Status()
	}
	item.Status, item.ReasonKey = state.Status, state.ReasonKey
	return item
}

func (engines *Engines) entry(id config.RuntimeID) (*engineEntry, error) {
	entry := engines.entries[id]
	if entry == nil {
		if id != config.RuntimeNative {
			return nil, ErrEngineNotFound
		}
		return nil, conversationconfig.ErrRuntimeCapabilityUnsupported
	}
	return entry, nil
}

func (entry *engineEntry) connect(ctx context.Context) error {
	if entry.closed {
		return ErrEngineNotReady
	}
	if entry.connection != nil && entry.connection.Status().Status != "unavailable" {
		return nil
	}
	if entry.users != 0 {
		return ErrOperationActive
	}
	if entry.connection != nil {
		_ = entry.connection.Close()
		entry.connection = nil
		entry.models, entry.modelsExpiry = external.Models{}, time.Time{}
	}
	connection, err := entry.factory(ctx)
	if err != nil {
		entry.state = external.ConnectionState{Status: "unavailable", ReasonKey: "agentRuntime.connectionFailed"}
		if errors.Is(err, ErrEngineNotInstalled) {
			entry.state = external.ConnectionState{Status: "not_installed", ReasonKey: "agentRuntime.notInstalled"}
		}
		if key := VersionUnsupportedReasonKey(err); key != "" {
			entry.state = external.ConnectionState{Status: "incompatible", ReasonKey: key}
		}
		if entry.id == config.RuntimeClaude && entry.state.Status == "not_installed" {
			entry.state.ReasonKey = "agentRuntime.claudeNotInstalled"
		}
		if entry.id == config.RuntimeGrok && entry.state.Status == "not_installed" {
			entry.state.ReasonKey = "agentRuntime.grokNotInstalled"
		}
		if entry.id == config.RuntimeAntigravity && entry.state.Status == "not_installed" {
			entry.state.ReasonKey = "agentRuntime.antigravityNotInstalled"
		}
		return err
	}
	entry.connection = connection
	return nil
}

func (engines *Engines) Check(ctx context.Context, id config.RuntimeID) (EngineDescriptor, error) {
	if id == config.RuntimeNative {
		return engineDescriptor(id), nil
	}
	entry, err := engines.entry(id)
	if err != nil {
		return EngineDescriptor{}, err
	}
	entry.mu.Lock()
	defer entry.mu.Unlock()
	entry.models, entry.modelsExpiry = external.Models{}, time.Time{}
	// CLI login and configuration changes happen outside this process. An
	// explicit idle check reloads them; accepted operations retain their lease.
	if entry.connection != nil && entry.users == 0 {
		_ = entry.connection.Close()
		entry.connection = nil
		entry.models, entry.modelsExpiry = external.Models{}, time.Time{}
	}
	if err := entry.connect(ctx); err != nil {
		return entry.descriptor(), nil
	}
	state, err := entry.connection.Check(ctx)
	if err != nil && state.Status != "ready" && state.ReasonKey == "" {
		state = external.ConnectionState{Status: "unavailable", ReasonKey: "agentRuntime.connectionFailed"}
	}
	if key := VersionUnsupportedReasonKey(err); key != "" {
		state = external.ConnectionState{Status: "incompatible", ReasonKey: key}
	}
	entry.state = state
	item := engineDescriptor(id)
	item.Status, item.ReasonKey = state.Status, state.ReasonKey
	return item, nil
}

func (engines *Engines) Models(ctx context.Context, id config.RuntimeID) (external.Models, error) {
	entry, err := engines.entry(id)
	if err != nil {
		return external.Models{}, err
	}
	entry.mu.Lock()
	defer entry.mu.Unlock()
	if err := entry.connect(ctx); err != nil {
		return external.Models{}, err
	}
	// Only an unchecked connection probes here, so the first catalog read also
	// establishes readiness. Ready connections never re-probe: doing that made
	// every menu open spawn a CLI process, and one transient failure turned a
	// working runtime into an unavailable one.
	state := entry.connection.Status()
	if state.Status == "unchecked" {
		if _, err := entry.connection.Check(ctx); err != nil {
			return external.Models{}, err
		}
		state = entry.connection.Status()
	}
	if state.Status != "ready" {
		return external.Models{}, ErrEngineNotReady
	}
	if time.Now().Before(entry.modelsExpiry) {
		return entry.models, nil
	}
	models, err := entry.connection.Models(ctx)
	if err != nil {
		return external.Models{}, err
	}
	entry.models, entry.modelsExpiry = models, time.Now().Add(engineModelsTTL)
	return models, nil
}

// Acquire pins the connection from preflight through durable settlement. The
// caller releases on acceptance failure or after Wait, including cancellation.
func (engines *Engines) Acquire(ctx context.Context, selection config.RuntimeSelection, cfg config.Config) (external.Adapter, func(), error) {
	entry, err := engines.entry(selection.Kind)
	if err != nil {
		return nil, nil, err
	}
	if err := selection.Validate(config.AgentKindGeneral); err != nil {
		return nil, nil, err
	}
	if selection.ModelProfileID() != "" {
		return engines.acquireAPI(ctx, selection, cfg)
	}
	entry.mu.Lock()
	defer entry.mu.Unlock()
	if err := entry.connect(ctx); err != nil {
		return nil, nil, err
	}
	state, err := entry.connection.Check(ctx)
	if err != nil {
		return nil, nil, err
	}
	if state.Status != "ready" {
		return nil, nil, ErrEngineNotReady
	}
	models, err := entry.connection.Models(ctx)
	if err != nil {
		return nil, nil, err
	}
	var modelID, effort string
	switch selection.Kind {
	case config.RuntimeCodex:
		modelID, effort = selection.Codex.Model, selection.Codex.Effort
	case config.RuntimeClaude:
		modelID, effort = selection.Claude.Model, selection.Claude.Effort
	case config.RuntimeGrok:
		modelID, effort = selection.Grok.Model, selection.Grok.Effort
	case config.RuntimeAntigravity:
		modelID, effort = selection.Antigravity.Model, selection.Antigravity.Effort
	default:
		return nil, nil, ErrEngineNotFound
	}
	valid := false
	for _, model := range models.Items {
		if model.ID == modelID && (effort == "" || slices.Contains(model.Efforts, effort)) {
			valid = true
			break
		}
	}
	if !valid {
		return nil, nil, ErrEngineModelUnavailable
	}
	entry.users++
	var once sync.Once
	return entry.connection, func() { once.Do(func() { entry.mu.Lock(); entry.users--; entry.mu.Unlock() }) }, nil
}

func (engines *Engines) Close() error {
	var failures []error
	engines.apiMu.Lock()
	engines.apiClosed = true
	for connection := range engines.apiConnections {
		failures = append(failures, connection.Close())
		delete(engines.apiConnections, connection)
	}
	engines.apiMu.Unlock()
	for _, entry := range engines.entries {
		entry.mu.Lock()
		entry.closed = true
		entry.models, entry.modelsExpiry = external.Models{}, time.Time{}
		if entry.connection != nil {
			if err := entry.connection.Close(); err != nil {
				failures = append(failures, fmt.Errorf("close %s: %w", entry.id, err))
			}
		}
		entry.mu.Unlock()
	}
	return errors.Join(failures...)
}
