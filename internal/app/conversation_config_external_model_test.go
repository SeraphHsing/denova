package app

import (
	"encoding/json"
	"reflect"
	"testing"

	"denova/config"
	"denova/internal/agents/conversationconfig"
)

func TestConversationConfigPatchPreservesExternalModelSelection(t *testing.T) {
	tests := []struct {
		name string
		kind config.RuntimeID
		input string
	}{
		{name: "grok", kind: config.RuntimeGrok, input: `{"grok":{"model":"grok-test","effort":"high"}}`},
		{name: "antigravity", kind: config.RuntimeAntigravity, input: `{"antigravity":{"model":"antigravity-test","effort":"high"}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var patch ConversationConfigPatch
			if err := json.Unmarshal([]byte(tt.input), &patch); err != nil {
				t.Fatal(err)
			}
			next, err := json.Marshal(patch)
			if err != nil {
				t.Fatal(err)
			}
			var parsed conversationconfig.Patch
			if err := json.Unmarshal(next, &parsed); err != nil {
				t.Fatal(err)
			}
			switch tt.kind {
			case config.RuntimeGrok:
				if parsed.Grok == nil || *parsed.Grok != (config.GrokRuntimeSettings{Model: "grok-test", Effort: "high"}) {
					t.Fatalf("Grok patch lost at app transport boundary: %#v", parsed)
				}
			case config.RuntimeAntigravity:
				if parsed.Antigravity == nil || *parsed.Antigravity != (config.AntigravityRuntimeSettings{Model: "antigravity-test", Effort: "high"}) {
					t.Fatalf("Antigravity patch lost at app transport boundary: %#v", parsed)
				}
			}
			roundTrip, err := json.Marshal(parsed)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(next, roundTrip) {
				t.Fatalf("app patch changed conversation semantics: %s != %s", next, roundTrip)
			}
		})
	}
}

func TestConversationConfigPatchRejectsNullExternalModelSettings(t *testing.T) {
	for _, input := range []string{`{"grok":null}`, `{"antigravity":null}`} {
		var patch ConversationConfigPatch
		if err := json.Unmarshal([]byte(input), &patch); err == nil {
			t.Fatalf("accepted a null model settings patch: %s", input)
		}
	}
}
