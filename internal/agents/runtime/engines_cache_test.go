package agentruntime

import (
	"context"
	"errors"
	"testing"
	"time"

	"denova/config"
	"denova/internal/agents/runtime/external"
)

func TestModelCacheExpiresAndCannotOutliveEngineClose(t *testing.T) {
	engines := NewEngines()
	connection := &testConnection{state: external.ConnectionState{Status: "ready"}}
	engines.entries[config.RuntimeGrok].factory = func(context.Context) (external.Connection, error) { return connection, nil }
	for range 2 {
		if _, err := engines.Models(t.Context(), config.RuntimeGrok); err != nil {
			t.Fatal(err)
		}
	}
	if connection.models != 1 || connection.probes != 0 {
		t.Fatalf("cached catalog reprobed: models=%d probes=%d", connection.models, connection.probes)
	}
	engines.entries[config.RuntimeGrok].modelsExpiry = time.Unix(1, 0)
	if _, err := engines.Models(t.Context(), config.RuntimeGrok); err != nil || connection.models != 2 {
		t.Fatalf("expired catalog was not refreshed: models=%d err=%v", connection.models, err)
	}
	if err := engines.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := engines.Models(t.Context(), config.RuntimeGrok); !errors.Is(err, ErrEngineNotReady) {
		t.Fatalf("closed engine returned cached models: %v", err)
	}
}

func TestExplicitCheckInvalidatesModelsWithoutReplacingLeasedConnection(t *testing.T) {
	engines := NewEngines()
	defer engines.Close()
	connection := &testConnection{state: external.ConnectionState{Status: "ready"}}
	entry := engines.entries[config.RuntimeGrok]
	entry.factory = func(context.Context) (external.Connection, error) { return connection, nil }
	if _, err := engines.Models(t.Context(), config.RuntimeGrok); err != nil {
		t.Fatal(err)
	}
	entry.users = 1
	if _, err := engines.Check(t.Context(), config.RuntimeGrok); err != nil {
		t.Fatal(err)
	}
	if _, err := engines.Models(t.Context(), config.RuntimeGrok); err != nil || connection.models != 2 || connection.closes != 0 {
		t.Fatalf("leased check did not refresh catalog safely: models=%d closes=%d err=%v", connection.models, connection.closes, err)
	}
	connection.state = external.ConnectionState{Status: "auth_required"}
	if _, err := engines.Models(t.Context(), config.RuntimeGrok); !errors.Is(err, ErrEngineNotReady) {
		t.Fatalf("signed-out engine returned cached models: %v", err)
	}
	entry.users = 0
}
