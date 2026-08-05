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

This repository holds **two binaries**:

- **`cmd/lfpweather-agent`** — the agent. Runs inside a Kubernetes Agent
  Sandbox, one instance per conversation. It reaches the MCP server over
  in-cluster DNS; the only egress off the cluster is the Anthropic API call.
- **`cmd/lfpweather-broker`** — the broker. A long-lived Deployment that maps a
  browser session to an agent and reverse-proxies the streaming chat. See
  [Broker](#broker) below.

The rest of this document describes the agent; the broker has its own section.

## HTTP API

| Method + path | Purpose |
|---|---|
| `POST /v1/chat` | Stream the answer to one user message as Server-Sent Events. Body: `{"session_id": "...", "message": "..."}`. |
| `GET /healthz`, `GET /readyz` | Liveness / readiness. |
| `GET /usage` | Cumulative token usage since process start (JSON) — basis for budget monitoring. |

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
| `MAX_TURNS` | `8` | Max tool-use iterations per user message (bounds worst-case token spend). |
| `RATE_LIMIT_RPM` | `20` | Per-IP requests per minute on `/v1/chat`. `<= 0` disables limiting. |
| `RATE_LIMIT_BURST` | `5` | Per-IP burst size. |
| `TRUST_FORWARDED_FOR` | `false` | Use the left-most `X-Forwarded-For` as the client IP. Enable only behind a trusted proxy/broker. |
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

## Broker

`cmd/lfpweather-broker` sits between the frontend and the agents. For each
`POST /v1/chat`, it resolves the `session_id` to an agent and reverse-proxies
the request, streaming the SSE response straight back (no buffering).

Two provider modes:

- **`direct`** — every session proxies to a single `AGENT_URL`. For local
  development and testing against one agent process.
- **`sandbox`** — each session gets its own Agent Sandbox
  ([kubernetes-sigs/agent-sandbox](https://github.com/kubernetes-sigs/agent-sandbox)).
  The broker creates a sandbox on first message, reaches the agent at the pod IP
  on `AGENT_PORT`, reuses it for the session, and the idle reaper deletes
  sandboxes for closed tabs. Requires a network policy allowing broker → sandbox
  pod and sandbox → MCP / Anthropic.

### Broker configuration

| Variable | Default | Purpose |
|---|---|---|
| `PROVIDER_MODE` | `direct` | `direct` or `sandbox`. |
| `AGENT_URL` | — | Direct mode: the single agent to proxy to. |
| `WARM_POOL_NAME` | — | Sandbox mode: the `SandboxWarmPool` to draw from (required). |
| `NAMESPACE` | `default` | Sandbox mode: namespace for the sandbox claims. |
| `AGENT_PORT` | `8080` | Sandbox mode: the agent's HTTP port inside the pod. |
| `SANDBOX_ROUTER_URL` | — | Sandbox mode: in-cluster sandbox-router URL (SDK `APIURL`). |
| `SANDBOX_IDLE_TTL` | `15m` | Sandbox mode: delete a session's sandbox after this idle time. |
| `MAX_BODY_BYTES` | `65536` | Max chat request body. |
| `PORT` | `8080` | HTTP listen port. |

Run the broker in direct mode against a local agent:

```sh
PROVIDER_MODE=direct AGENT_URL=http://localhost:8091 PORT=8092 \
  go run ./cmd/lfpweather-broker
```

The Dockerfile builds both binaries into the image; the broker Deployment
overrides the entrypoint with `/usr/local/bin/lfpweather-broker`.

> **Status:** the broker's proxy, provider abstraction, and direct mode are done
> and tested end to end (broker → agent → MCP). The sandbox provider is
> implemented against the Agent Sandbox Go SDK and compiles, but has not yet
> been validated against a live cluster.

## Cost controls

- **Prompt caching** — the system prompt and tool schemas (the fixed prefix that
  is re-sent on every turn of the tool-use loop) are cached, cutting input cost.
- **Usage accounting** — every model response's token usage is tracked; the
  running total is served at `GET /usage`.
- **Bounds** — `MAX_TURNS` caps the tool loop, `ANTHROPIC_MAX_TOKENS` caps output
  per turn, and per-IP rate limiting caps request frequency.

## Status

Runtime with the Claude tool-use loop, MCP client, session store, streaming HTTP
API, prompt caching, usage accounting, and per-IP rate limiting. Not yet included
(follow-ups): OpenTelemetry via `ootel` to match the other services, a global
daily token budget with graceful degrade (planned for the broker, which sees all
sessions), and CI + release automation.
