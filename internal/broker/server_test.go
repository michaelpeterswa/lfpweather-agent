package broker

import (
	"bufio"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeAgent is a stand-in agent that streams two SSE frames.
func fakeAgent(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl, _ := w.(http.Flusher)
		_, _ = w.Write([]byte("event: token\ndata: {\"text\":\"hi\"}\n\n"))
		if fl != nil {
			fl.Flush()
		}
		_, _ = w.Write([]byte("event: done\ndata: {}\n\n"))
	}))
}

func TestProxyStreamsFromAgent(t *testing.T) {
	agent := fakeAgent(t)
	defer agent.Close()

	h := NewServer(NewDirectProvider(agent.URL), 65536).Handler()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat", strings.NewReader(`{"session_id":"tab-1","message":"hi"}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("content-type = %q, want text/event-stream", ct)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "event: token") || !strings.Contains(body, `"text":"hi"`) {
		t.Errorf("body missing token frame: %q", body)
	}
	if !strings.Contains(body, "event: done") {
		t.Errorf("body missing done frame: %q", body)
	}
	// Sanity: frames are separated by blank lines.
	sc := bufio.NewScanner(strings.NewReader(body))
	var frames int
	for sc.Scan() {
		if strings.HasPrefix(sc.Text(), "event:") {
			frames++
		}
	}
	if frames != 2 {
		t.Errorf("got %d event frames, want 2", frames)
	}
}

func TestChatValidation(t *testing.T) {
	h := NewServer(NewDirectProvider("http://unused"), 65536).Handler()

	tests := []struct {
		name string
		body string
		want int
	}{
		{"missing session_id", `{"message":"hi"}`, http.StatusBadRequest},
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
	h := NewServer(NewDirectProvider("http://unused"), 65536).Handler()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

func TestDirectProviderAcquire(t *testing.T) {
	p := NewDirectProvider("http://agent:8080")
	got, err := p.Acquire(t.Context(), "any-session")
	if err != nil {
		t.Fatalf("Acquire error: %v", err)
	}
	if got != "http://agent:8080" {
		t.Errorf("target = %q, want http://agent:8080", got)
	}
}
