# AGENTS.md

qodercn-gateway — a lightweight, remote-only proxy that re-exposes the QoderCN
Remote API through OpenAI/Anthropic-compatible HTTP endpoints.

## Commands

```bash
go build ./...            # build
go vet ./...              # vet
go test -race ./...       # test
gofmt -w cmd internal     # format
go run ./cmd/qodercn-gateway --port 8095
```

## Architecture

- `cmd/qodercn-gateway/` — entry point, flags/config/env plumbing
- `internal/httpapi/` — HTTP API layer (OpenAI + Anthropic routing); optional
  server-side tool injection isolated in `servertools_*.go` behind a one-line
  seam in each chat handler
- `internal/remote/` — QoderCN Remote API client: cosy request signing, SSE
  chat, image search/gen, credential loading
- `internal/service/` — request orchestration (prompt build, streaming, output limiter)
- `internal/tooltypes/` — shared tool data types + request-side extractors
- `internal/deploy/` — credential / server-bundle export

## Conventions

- Remote-only and dependency-free (Go stdlib): keep `go.mod` without a `require`
  block. Do not reintroduce a local IPC/IDE transport, a desktop GUI, or
  multi-model fallback.
- The gateway uses native function-calling; there is no prompt-injection tool
  emulation. `EmulatesTextTools` is always false.
- Reply to the user in Chinese; keep code comments in English.
