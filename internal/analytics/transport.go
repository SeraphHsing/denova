package analytics

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"runtime"
	"time"
)

const runtimeOS = runtime.GOOS

// The public capture API lets the existing bounded queue own cancellation and
// opt-out. A second SDK queue would retain data after our queue was discarded.
// https://posthog.com/docs/api/capture
func (s *Service) send(item queuedEvent) {
	s.mu.Lock()
	if !s.collectingLocked() || item.generation != s.generation {
		s.mu.Unlock()
		return
	}
	ctx, cancel := context.WithTimeout(s.sendContext, 5*time.Second)
	s.mu.Unlock()
	defer cancel()
	data, err := json.Marshal(item.event)
	if err != nil {
		slog.Error("analytics_encode_failed", "event", item.event.Event)
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, bytes.NewReader(data))
	if err != nil {
		slog.Warn("analytics_request_failed", "event", item.event.Event)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Denova/"+s.version)
	response, err := s.client.Do(req)
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("analytics_delivery_failed", "event", item.event.Event)
		}
		return
	}
	defer response.Body.Close()
	var receipt struct {
		Status       json.RawMessage `json:"status"`
		QuotaLimited []string        `json:"quota_limited"`
	}
	decodeErr := json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&receipt)
	accepted := string(receipt.Status) == `1` || string(receipt.Status) == `"Ok"`
	if response.StatusCode < 200 || response.StatusCode >= 300 || decodeErr != nil || !accepted || len(receipt.QuotaLimited) != 0 {
		slog.Warn("analytics_delivery_rejected", "event", item.event.Event, "status", response.StatusCode)
	}
}
