# lfpweather-agent

Conversational agent runtime for [lfpweather.com](https://lfpweather.com). It
answers free-form questions about the Lake Forest Park weather station by
running a Claude tool-use loop over the tools exposed by
[lfpweather-mcp](https://github.com/michaelpeterswa/lfpweather-mcp).

One browser tab is one conversation. State lives only in memory for the life of
the tab (evicted after an idle TTL); nothing is persisted.

## How it fits

```
Browser ── SSE ── Next route handler ── SSE ── broker ── SSE ── lfpweather-agent (this)
                                                                  │  Anthropic Messages API (tool-use loop)
                                                                  └─ lfpweather-mcp (streamable HTTP) ── lfpweather-api
```

This service is the agent itself. It is designed to run inside a Kubernetes
Agent Sandbox, one instance per conversation, fronted by a broker that manages
sandbox lifecycle. It reaches the MCP server over in-cluster DNS; the only
egress off the cluster is the Anthropic API call.

## HTTP API

| Method + path | Purpose |
|---|---|
| `POST /v1/chat` | Stream the answer to one user message as Server-Sent Events. Body: `{"session_id": "...", "message": "..."}`. |
| `GET /healthz`, `GET /readyz` | Liveness / readiness. |

### `POST /v1/chat` events

Each SSE frame is `event: <type>` with a JSON `data:` line:

| `type` | Data | Meaning |
|---|---|---|
| `token` | `{"text": "..."}` | A chunk of assistant text. |
| `tool` | `{"tool": "query_weather", "status": "start"\|"done"}` | A tool call started or finished. |
| `done` | `{}` | The answer is complete. |
| `error` | `{"text": "..."}` | The turn failed. |

The same `session_id` continues a conversation; a request is processed one at a
time per session.

## Configuration

All configuration is environment variables.

| Variable | Default | Purpose |
|---|---|---|
| `ANTHROPIC_API_KEY` | — (required) | Anthropic API key. |
| `ANTHROPIC_MODEL` | `claude-sonnet-5` | Model. Sonnet is the cost-effective default for a public chat; set `claude-opus-5` for higher quality. |
| `ANTHROPIC_MAX_TOKENS` | `4096` | Max output tokens per turn. |
| `MCP_URL` | — (required) | Streamable-HTTP endpoint of lfpweather-mcp, e.g. `http://lfpweather-mcp.lfpweather.svc.cluster.local:80/mcp`. |
| `MCP_BEARER_TOKEN` | — | Bearer token for the MCP server, if it requires one. |
| `SYSTEM_PROMPT` | built-in | Override the system prompt. |
| `MAX_TURNS` | `8` | Max tool-use iterations per user message. |
| `SESSION_TTL` | `30m` | Idle lifetime of an in-memory conversation. |
| `REQUEST_TIMEOUT` | `90s` | End-to-end bound on one user message. |
| `SHUTDOWN_TIMEOUT` | `15s` | Graceful drain on SIGTERM. |
| `PORT` | `8080` | HTTP listen port. |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, or `error`. |

## Run locally

Point it at a running lfpweather-mcp server:

```sh
ANTHROPIC_API_KEY=sk-ant-... \
MCP_URL=http://localhost:8080/mcp \
PORT=8091 \
go run ./cmd/lfpweather-agent
```

Then:

```sh
curl -N http://localhost:8091/v1/chat \
  -H 'content-type: application/json' \
  -d '{"session_id":"tab-1","message":"what is the record high this year?"}'
```

## Status

First cut of the runtime: the Claude tool-use loop, MCP client, session store,
and streaming HTTP API. Not yet included (follow-ups): OpenTelemetry via
`ootel` to match the other services, request rate limiting / abuse controls,
and CI + release automation.
