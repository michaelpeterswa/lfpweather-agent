// Package server exposes the agent over HTTP: a streaming chat endpoint and
// health probes.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/michaelpeterswa/lfpweather-agent/internal/agent"
	"github.com/michaelpeterswa/lfpweather-agent/internal/session"
)

// Server wires the agent, the session store, and the HTTP handlers.
type Server struct {
	agent   *agent.Agent
	store   *session.Store
	timeout time.Duration
}

// New builds a Server.
func New(a *agent.Agent, store *session.Store, timeout time.Duration) *Server {
	return &Server{agent: a, store: store, timeout: timeout}
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

	ctx, cancel := context.WithTimeout(r.Context(), s.timeout)
	defer cancel()

	newHistory, err := s.agent.Run(ctx, sess.History, req.Message, emit)
	if err != nil {
		slog.Error("agent run failed", slog.String("session", req.SessionID), slog.String("error", err.Error()))
		emit(agent.Event{Type: agent.EventError, Text: "the assistant hit an error"})
		return
	}
	sess.History = newHistory
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
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
