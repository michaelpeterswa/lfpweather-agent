package agent

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

func TestTruncateToolResult(t *testing.T) {
	short := "small result"
	if got := truncateToolResult(short); got != short {
		t.Errorf("short result changed: %q", got)
	}

	// Multi-byte runes straddling the cut must not be split.
	long := strings.Repeat("°F", maxToolResultBytes)
	got := truncateToolResult(long)
	if !utf8.ValidString(got) {
		t.Error("truncated result is not valid UTF-8")
	}
	if !strings.Contains(got, "[result truncated:") {
		t.Error("truncated result does not say it was truncated")
	}
	if len(got) > maxToolResultBytes+200 {
		t.Errorf("truncated result is %d bytes, want about %d", len(got), maxToolResultBytes)
	}
}

func TestBusyErrorMessage(t *testing.T) {
	if got := (&BusyError{RetryAfter: 30 * time.Second}).UserMessage(); !strings.Contains(got, "30 seconds") {
		t.Errorf("message = %q, want it to name the wait", got)
	}
	if got := (&BusyError{}).UserMessage(); !strings.Contains(got, "a few seconds") {
		t.Errorf("message = %q, want a generic wait", got)
	}
}

// fakeAnthropic answers every request with status and the given headers, and
// counts the requests it saw.
func fakeAnthropic(t *testing.T, status int, headers map[string]string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		for k, v := range headers {
			w.Header().Set(k, v)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func newTestAgent(baseURL string) *Agent {
	client := anthropic.NewClient(option.WithBaseURL(baseURL), option.WithAPIKey("test"))
	return New(client, Options{Model: "claude-sonnet-5", MaxTokens: 1024, MaxTurns: 2})
}

// A 429 used to sit silent for the whole request deadline while the SDK
// honoured retry-after. It must now fail at once, as a BusyError carrying the
// wait, after a single request.
func TestRunRateLimitedFailsFast(t *testing.T) {
	srv, calls := fakeAnthropic(t, http.StatusTooManyRequests, map[string]string{"Retry-After": "30"})
	a := newTestAgent(srv.URL)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := time.Now()
	_, err := a.Run(ctx, nil, "and yesterday?", func(Event) {})

	var busy *BusyError
	if !errors.As(err, &busy) {
		t.Fatalf("err = %v, want a *BusyError", err)
	}
	if busy.RetryAfter != 30*time.Second {
		t.Errorf("RetryAfter = %v, want 30s", busy.RetryAfter)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("took %v, want an immediate failure", d)
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("made %d requests, want 1 (no SDK retries)", n)
	}
}

// An overloaded API gets exactly one quick retry before the error surfaces.
func TestRunOverloadedRetriesOnce(t *testing.T) {
	srv, calls := fakeAnthropic(t, 529, nil)
	a := newTestAgent(srv.URL)

	_, err := a.Run(context.Background(), nil, "hi", func(Event) {})
	if err == nil {
		t.Fatal("want an error")
	}
	var busy *BusyError
	if errors.As(err, &busy) {
		t.Error("an overloaded error is not a rate limit")
	}
	if n := calls.Load(); n != 2 {
		t.Errorf("made %d requests, want 2 (one retry)", n)
	}
}

// Request bodies must carry the top-level cache marker so the history caches.
func TestRunSetsTopLevelCacheControl(t *testing.T) {
	var body atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := new(strings.Builder)
		_, _ = io.Copy(b, r.Body)
		body.Store(b.String())
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"stop"}}`))
	}))
	defer srv.Close()

	_, _ = newTestAgent(srv.URL).Run(context.Background(), nil, "hi", func(Event) {})
	got, _ := body.Load().(string)
	if !strings.Contains(got, `"cache_control":{"type":"ephemeral"}`) {
		t.Errorf("request body has no top-level cache_control: %s", got)
	}
}
