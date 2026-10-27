package analytics

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
)

type receiptTransport struct {
	status int
	body   string
}

func (r receiptTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: r.status, Body: io.NopCloser(strings.NewReader(r.body)), Header: make(http.Header)}, nil
}

func TestCaptureRequiresSuccessfulReceiptAndNoQuotaLimit(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   int
		body     string
		rejected bool
	}{
		{"cloud receipt", 200, `{"status":"Ok"}`, false},
		{"numeric receipt", 200, `{"status":1}`, false},
		{"http only", 200, `{}`, true},
		{"quota limited", 200, `{"status":"Ok","quota_limited":["events"]}`, true},
		{"rejected", 400, `{"status":1}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var output bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
			defer slog.SetDefault(previous)
			s := &Service{available: true, state: state{Enabled: true, NoticeSeen: true, ID: "test"}, sendContext: context.Background(),
				client: &http.Client{Transport: receiptTransport{status: tc.status, body: tc.body}}, endpoint: "https://collector.example/i/v0/e"}
			s.send(queuedEvent{event: event{Event: "test"}})
			if got := strings.Contains(output.String(), "analytics_delivery_rejected"); got != tc.rejected {
				t.Fatalf("receipt rejection=%v, want %v; logs=%s", got, tc.rejected, output.String())
			}
		})
	}
}
