# qodercn-gateway

A lightweight, remote-only gateway that exposes the QoderCN Remote API
(`gateway.qoder.com.cn`) through standard **OpenAI** and **Anthropic** HTTP APIs,
so clients like Claude Code, Cline, or any OpenAI/Anthropic-compatible tool can
talk to the QoderCN-hosted models (Qwen, Kimi, MiniMax, GLM, DeepSeek, …).

It is a focused extraction of the remote backend from the original
`lingma-ipc-proxy`: no local IDE/IPC transport, no desktop GUI, no multi-model
fallback — just a transparent single-model proxy. **Zero third-party
dependencies (pure Go standard library).**

## Endpoints

| Method | Path | Purpose |
| --- | --- | --- |
| POST | `/v1/chat/completions`, `/api/v1/chat/completions` | OpenAI Chat Completions |
| POST | `/v1/messages` | Anthropic Messages |
| GET  | `/v1/models` | Model list |
| POST | `/v1/images/search` | Image search (gateway `imageSearch`) |
| POST | `/v1/images/generations` | Image generation (gateway `generateImage`) |
| GET  | `/quota`, `/v1/quota` | Account credit/usage snapshot |
| GET  | `/version` | Build version |
| GET  | `/`, `/health` | Health / status |

Chat and Messages stream via SSE. The gateway supports native function-calling,
so tools are forwarded natively (no prompt-injection emulation).

## Build & run

```bash
go build -o qodercn-gateway ./cmd/qodercn-gateway
./qodercn-gateway --host 127.0.0.1 --port 8095
```

Credentials are read from the local QoderCN CLI login cache automatically, or
from an explicit `--remote-auth-file credentials.json`. Export a portable
credential/deployment bundle for a server:

```bash
./qodercn-gateway --export-server-bundle bundle.zip
```

## Configuration

Flags (see `--help`), a JSON config file (`--config qodercn-gateway.json`, see
`config.example.json`), or environment variables — flags override env override
file. Key knobs: `--remote-auth-file`, `--remote-base-url`, `--remote-proxy-url`,
`--model`, `--auth-keys-file` (inbound API-key allowlist; empty = open).

Optional feature flags (env):

- `QODERCN_INJECT_MEDIA_TOOLS=1` — advertise and run the gateway's
  `web_search` / `ImageSearch` / `TextPolish` tools server-side in an agentic
  loop (hidden from the client). The whole feature lives in
  `internal/httpapi/servertools_*.go` behind a single per-handler seam.
- `QODERCN_IMAGE_DEWATERMARK=1` — destroy the robust watermark payload on
  generated images via a non-invertible geometric desync re-encode.

## Docker

```bash
docker build -t qodercn-gateway .
docker run -p 8095:8095 -v "$PWD/credentials.json:/credentials.json:ro" \
  qodercn-gateway --remote-auth-file /credentials.json
```

## Layout

- `cmd/qodercn-gateway` — entry point / config plumbing
- `internal/httpapi` — OpenAI + Anthropic HTTP surface (+ optional server tools)
- `internal/remote` — QoderCN Remote API client (cosy signing, SSE, images, credentials)
- `internal/service` — request orchestration
- `internal/toolemulation` — tool data types + request-side extractors
- `internal/deploy` — credential / server-bundle export
