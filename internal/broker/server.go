package broker

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// Server proxies streaming chat from the frontend to the per-session agent.
type Server struct {
	provider Provider
	maxBody  int64
	budget   *BudgetTracker // nil disables the daily budget
	metrics  *metrics
	tracer   trace.Tracer
}

// NewServer builds a broker HTTP server. A nil budget disables the daily
// token ceiling and reports zeros on /usage.
func NewServer(provider Provider, maxBody int64, budget *BudgetTracker) *Server {
	return &Server{
		provider: provider,
		maxBody:  maxBody,
		budget:   budget,
		metrics:  newMetrics(budget),
		tracer:   otel.Tracer("lfpweather-broker"),
	}
}

// Handler returns the HTTP handler with all routes registered.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat", s.handleChat)
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("GET /readyz", s.handleHealth)
	mux.HandleFunc("GET /usage", s.handleUsage)
	return mux
}

type chatRequest struct {
	SessionID string `json:"session_id"`
}

// handleChat resolves the session's agent and reverse-proxies the request,
// streaming the Server-Sent Events straight back to the caller.
func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	// Continue any trace the frontend started, then span this request. The span
	// is created by hand rather than by wrapping the handler with otelhttp, so
	// the streaming ResponseWriter keeps its http.Flusher for SSE.
	ctx := otel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header))
	ctx, span := s.tracer.Start(ctx, "broker.chat")
	defer span.End()
	r = r.WithContext(ctx)

	body, err := io.ReadAll(io.LimitReader(r.Body, s.maxBody))
	if err != nil {
		http.Error(w, "could not read body", http.StatusBadRequest)
		return
	}

	var req chatRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.SessionID == "" {
		http.Error(w, "session_id is required", http.StatusBadRequest)
		return
	}
	span.SetAttributes(attribute.String("session.id", req.SessionID))

	// Daily budget: past the ceiling, degrade gracefully instead of spending
	// more (and instead of spinning up a fresh sandbox).
	if s.budget != nil && s.budget.Exceeded() {
		s.metrics.recordChat(ctx, "degraded")
		writeDegrade(w)
		return
	}

	claimStart := time.Now()
	target, err := s.provider.Acquire(ctx, req.SessionID)
	s.metrics.observeClaim(ctx, time.Since(claimStart), err)
	if err != nil {
		s.metrics.recordChat(ctx, "acquire_error")
		span.RecordError(err)
		span.SetStatus(codes.Error, "acquire agent")
		slog.Error("could not acquire agent", slog.String("session", req.SessionID), slog.String("error", err.Error()))
		http.Error(w, "no agent available", http.StatusBadGateway)
		return
	}

	base, err := url.Parse(target)
	if err != nil {
		s.metrics.recordChat(ctx, "acquire_error")
		slog.Error("bad agent target", slog.String("target", target), slog.String("error", err.Error()))
		http.Error(w, "bad agent target", http.StatusInternalServerError)
		return
	}

	// Restore the body we consumed to sniff the session id, then reverse-proxy.
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))

	proxied := true
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme = base.Scheme
			pr.Out.URL.Host = base.Host
			pr.Out.URL.Path = "/v1/chat"
			pr.Out.Host = base.Host
		},
		// Propagate trace context to the agent and record the upstream call.
		Transport: otelhttp.NewTransport(http.DefaultTransport),
		// Flush every write immediately so SSE frames are not buffered.
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, pr *http.Request, err error) {
			proxied = false
			s.metrics.recordChat(pr.Context(), "proxy_error")
			span.RecordError(err)
			span.SetStatus(codes.Error, "proxy to agent")
			slog.Error("proxy to agent failed", slog.String("target", target), slog.String("error", err.Error()))
			http.Error(w, "agent unreachable", http.StatusBadGateway)
		},
	}
	proxy.ServeHTTP(w, r)
	if proxied {
		s.metrics.recordChat(ctx, "ok")
	}
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

// handleUsage reports the daily token budget state.
func (s *Server) handleUsage(w http.ResponseWriter, _ *http.Request) {
	snap := BudgetSnapshot{Remaining: -1}
	if s.budget != nil {
		snap = s.budget.Snapshot()
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(snap); err != nil {
		slog.Error("failed to write usage", slog.String("error", err.Error()))
	}
}

// writeDegrade returns a well-formed SSE stream carrying a friendly "resting"
// message, so the chat UI renders it like any other reply.
func writeDegrade(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.WriteHeader(http.StatusOK)
	const msg = `{"type":"error","text":"The assistant is resting for today — check back tomorrow, or explore the dashboard."}`
	_, _ = io.WriteString(w, "event: error\ndata: "+msg+"\n\n")
	_, _ = io.WriteString(w, "event: done\ndata: {}\n\n")
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}
