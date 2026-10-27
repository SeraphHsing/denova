package api

import (
	"denova/internal/analytics"
	"net/http"
	"testing"
)

func TestAnalyticsAPIRejectsContentAndKeepsHostPreferencesIndependentOfProjects(t *testing.T) {
	application := newTestApplication(t)
	application.StartAnalytics("dev")
	server := NewServer(application, "0")
	response := performJSONRequest(t, server, http.MethodGet, "/api/analytics", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d", response.Code)
	}
	var preferences analytics.Preferences
	decodeResponse(t, response.Body.Bytes(), &preferences)
	if !preferences.Enabled || !preferences.NoticeSeen || preferences.Available {
		t.Fatalf("development preferences=%+v", preferences)
	}
	for _, body := range []any{
		map[string]string{"feature": "writing", "content": "private manuscript"},
		map[string]string{"feature": "private manuscript"},
		map[string]string{"feature": "game", "url": "/private/project"},
	} {
		rejected := performJSONRequest(t, server, http.MethodPost, "/api/analytics/activity", body)
		if rejected.Code != http.StatusBadRequest {
			t.Fatalf("unsafe request accepted: %s", rejected.Body.String())
		}
	}
	for _, feature := range []string{"writing", "game"} {
		activity := performJSONRequest(t, server, http.MethodPost, "/api/analytics/activity", map[string]string{"feature": feature})
		if activity.Code != http.StatusOK {
			t.Fatalf("safe activity rejected: %s", activity.Body.String())
		}
	}
	for _, path := range []string{"/api/analytics/open", "/api/analytics/error"} {
		body := map[string]string{}
		if path == "/api/analytics/error" {
			body["error_code"] = "react_error"
		}
		accepted := performJSONRequest(t, server, http.MethodPost, path, body)
		if accepted.Code != http.StatusOK {
			t.Fatalf("safe telemetry rejected: %s", accepted.Body.String())
		}
		body["content"] = "private manuscript"
		rejected := performJSONRequest(t, server, http.MethodPost, path, body)
		if rejected.Code != http.StatusBadRequest {
			t.Fatal("telemetry admitted arbitrary content")
		}
	}
	for _, code := range []string{"unknown", "authentication", "private manuscript"} {
		rejected := performJSONRequest(t, server, http.MethodPost, "/api/analytics/error", map[string]string{"error_code": code})
		if rejected.Code != http.StatusBadRequest {
			t.Fatal("telemetry admitted a non-browser error code")
		}
	}
	for _, enabled := range []bool{false, true} {
		updated := performJSONRequest(t, server, http.MethodPatch, "/api/analytics", map[string]bool{"enabled": enabled})
		if updated.Code != http.StatusOK {
			t.Fatalf("preferences update failed: %s", updated.Body.String())
		}
		reloaded := performJSONRequest(t, server, http.MethodGet, "/api/analytics", nil)
		decodeResponse(t, reloaded.Body.Bytes(), &preferences)
		if preferences.Enabled != enabled {
			t.Fatalf("preference update was not visible")
		}
	}
	for _, body := range []any{map[string]bool{}, map[string]any{"enabled": nil}, map[string]any{"enabled": true, "website": "private"}} {
		rejected := performJSONRequest(t, server, http.MethodPatch, "/api/analytics", body)
		if rejected.Code != http.StatusBadRequest {
			t.Fatalf("invalid preference accepted")
		}
	}
}
