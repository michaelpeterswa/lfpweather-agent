package broker

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
)

// Server proxies streaming chat from the frontend to the per-session agent.
type Server struct {
	provider Provider
	maxBody  int64
}

// NewServer builds a broker HTTP server.
func NewServer(provider Provider, maxBody int64) *Server {
	return &Server{provider: provider, maxBody: maxBody}
}

// Handler returns the HTTP handler with all routes registered.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat", s.handleChat)
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("GET /readyz", s.handleHealth)
	return mux
}

type chatRequest struct {
	SessionID string `json:"session_id"`
}

// handleChat resolves the session's agent and reverse-proxies the request,
// streaming the Server-Sent Events straight back to the caller.
func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
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

	target, err := s.provider.Acquire(r.Context(), req.SessionID)
	if err != nil {
		slog.Error("could not acquire agent", slog.String("session", req.SessionID), slog.String("error", err.Error()))
		http.Error(w, "no agent available", http.StatusBadGateway)
		return
	}

	base, err := url.Parse(target)
	if err != nil {
		slog.Error("bad agent target", slog.String("target", target), slog.String("error", err.Error()))
		http.Error(w, "bad agent target", http.StatusInternalServerError)
		return
	}

	// Restore the body we consumed to sniff the session id, then reverse-proxy.
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))

	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme = base.Scheme
			pr.Out.URL.Host = base.Host
			pr.Out.URL.Path = "/v1/chat"
			pr.Out.Host = base.Host
		},
		// Flush every write immediately so SSE frames are not buffered.
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			slog.Error("proxy to agent failed", slog.String("target", target), slog.String("error", err.Error()))
			http.Error(w, "agent unreachable", http.StatusBadGateway)
		},
	}
	proxy.ServeHTTP(w, r)
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}
