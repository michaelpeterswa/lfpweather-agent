package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/michaelpeterswa/lfpweather-agent/internal/agent"
)

func TestChatValidation(t *testing.T) {
	// A nil agent is fine: these requests are rejected before the agent runs.
	h := New(nil, nil, 0).Handler()

	tests := []struct {
		name string
		body string
		want int
	}{
		{"missing session_id", `{"message":"hi"}`, http.StatusBadRequest},
		{"missing message", `{"session_id":"tab-1"}`, http.StatusBadRequest},
		{"invalid json", `{`, http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/v1/chat", strings.NewReader(tt.body))
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tt.want {
				t.Errorf("status = %d, want %d", rec.Code, tt.want)
			}
		})
	}
}

func TestHealth(t *testing.T) {
	h := New(nil, nil, 0).Handler()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

func TestWriteSSE(t *testing.T) {
	rec := httptest.NewRecorder()
	writeSSE(rec, agent.Event{Type: agent.EventToken, Text: "hi"})

	got := rec.Body.String()
	if !strings.HasPrefix(got, "event: token\n") {
		t.Errorf("frame = %q, want it to start with the event line", got)
	}
	if !strings.Contains(got, `"text":"hi"`) {
		t.Errorf("frame = %q, want it to carry the JSON data", got)
	}
	if !strings.HasSuffix(got, "\n\n") {
		t.Errorf("frame = %q, want it to end with a blank line", got)
	}
}
