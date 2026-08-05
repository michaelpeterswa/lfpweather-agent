// Package server exposes the agent over HTTP: a streaming chat endpoint and
// health probes.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/michaelpeterswa/lfpweather-agent/internal/agent"
	"github.com/michaelpeterswa/lfpweather-agent/internal/ratelimit"
	"github.com/michaelpeterswa/lfpweather-agent/internal/session"
	"github.com/michaelpeterswa/lfpweather-agent/internal/usage"
)

// Options configures a Server.
type Options struct {
	Timeout           time.Duration
	Limiter           *ratelimit.Limiter // nil disables rate limiting
	Usage             *usage.Tracker     // nil serves zeros on /usage
	TrustForwardedFor bool
}

// Server wires the agent, the session store, and the HTTP handlers.
type Server struct {
	agent       *agent.Agent
	store       *session.Store
	timeout     time.Duration
	limiter     *ratelimit.Limiter
	usage       *usage.Tracker
	trustXF     bool
	reqDuration metric.Float64Histogram
	tracer      trace.Tracer
}

// New builds a Server.
func New(a *agent.Agent, store *session.Store, opts Options) *Server {
	reqDuration, err := otel.Meter("lfpweather-agent").Float64Histogram(
		"agent.request.duration",
		metric.WithDescription("Time to answer one user message, by outcome."),
		metric.WithUnit("s"),
	)
	if err != nil {
		slog.Warn("could not create request duration histogram", slog.String("error", err.Error()))
	}
	return &Server{
		agent:       a,
		store:       store,
		timeout:     opts.Timeout,
		limiter:     opts.Limiter,
		usage:       opts.Usage,
		trustXF:     opts.TrustForwardedFor,
		reqDuration: reqDuration,
		tracer:      otel.Tracer("lfpweather-agent"),
	}
}

// recordRequest records how long one answered message took and its outcome
// (ok, error, or timeout).
func (s *Server) recordRequest(ctx context.Context, d time.Duration, outcome string) {
	if s.reqDuration == nil {
		return
	}
	s.reqDuration.Record(ctx, d.Seconds(), metric.WithAttributes(attribute.String("outcome", outcome)))
}

// Handler returns the HTTP handler with all routes registered.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat", s.rateLimited(s.handleChat))
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("GET /readyz", s.handleHealth)
	mux.HandleFunc("GET /usage", s.handleUsage)
	return mux
}

// rateLimited wraps a handler with per-client-IP token-bucket limiting. On a
// limit hit it returns 429 before any streaming starts.
func (s *Server) rateLimited(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.limiter != nil {
			ip := s.clientIP(r)
			if !s.limiter.Allow(ip) {
				slog.Warn("rate limited", slog.String("ip", ip))
				w.Header().Set("Retry-After", "5")
				http.Error(w, "too many requests", http.StatusTooManyRequests)
				return
			}
		}
		next(w, r)
	}
}

// clientIP resolves the caller's IP, honoring the left-most X-Forwarded-For
// entry only when the proxy is trusted.
func (s *Server) clientIP(r *http.Request) string {
	if s.trustXF {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			if first, _, ok := strings.Cut(xff, ","); ok {
				return strings.TrimSpace(first)
			}
			return strings.TrimSpace(xff)
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

type chatRequest struct {
	SessionID string `json:"session_id"`
	Message   string `json:"message"`
}

// handleChat streams the agent's answer to one user message as Server-Sent
// Events. Each event is `event: <type>` with a JSON data line.
func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	var req chatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.SessionID == "" {
		http.Error(w, "session_id is required", http.StatusBadRequest)
		return
	}
	if req.Message == "" {
		http.Error(w, "message is required", http.StatusBadRequest)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // disable proxy buffering
	w.WriteHeader(http.StatusOK)

	emit := func(ev agent.Event) {
		writeSSE(w, ev)
		flusher.Flush()
	}

	// One message at a time per conversation.
	sess := s.store.Get(req.SessionID)
	sess.Mu.Lock()
	defer sess.Mu.Unlock()

	// Continue the broker's trace, then span this request. The span is created by
	// hand rather than by wrapping the handler with otelhttp, so the streaming
	// ResponseWriter keeps its http.Flusher for SSE.
	reqCtx := otel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header))
	reqCtx, span := s.tracer.Start(reqCtx, "agent.chat", trace.WithAttributes(attribute.String("session.id", req.SessionID)))
	defer span.End()

	ctx, cancel := context.WithTimeout(reqCtx, s.timeout)
	defer cancel()

	start := time.Now()
	newHistory, err := s.agent.Run(ctx, sess.History, req.Message, emit)
	outcome := "ok"
	if err != nil {
		outcome = "error"
		if ctx.Err() == context.DeadlineExceeded {
			outcome = "timeout"
		}
		span.RecordError(err)
		span.SetStatus(codes.Error, outcome)
		slog.Error("agent run failed", slog.String("session", req.SessionID), slog.String("error", err.Error()))
		emit(agent.Event{Type: agent.EventError, Text: "the assistant hit an error"})
	} else {
		sess.History = newHistory
	}
	s.recordRequest(reqCtx, time.Since(start), outcome)
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

// handleUsage reports cumulative token usage since process start — the basis
// for budget monitoring.
func (s *Server) handleUsage(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(s.usage.Snapshot()); err != nil {
		slog.Error("failed to write usage", slog.String("error", err.Error()))
	}
}

// writeSSE encodes one event as an SSE frame: an event: line naming the type
// and a data: line carrying the full event as JSON.
func writeSSE(w http.ResponseWriter, ev agent.Event) {
	data, err := json.Marshal(ev)
	if err != nil {
		slog.Error("failed to marshal event", slog.String("error", err.Error()))
		return
	}
	_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Type, data)
}
