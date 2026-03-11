# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Commands

```bash
# Build all binaries to bin/
make build

# Run standard unit tests with race detector
make test

# Run long-running tests (chaos, leak detection, heartbeat, fairness)
make test-long

# Run stress tests (5-minute timeout)
make test-stress

# Run entire test suite
make test-all

# Lint
make lint

# Format
make fmt
```

### Running a single test

```bash
# Standard tests (no build tag needed)
go test -race ./pkg/multiplex/... -run TestMultiplex_Basic

# Long tests (require build tag)
go test -race -tags=long ./pkg/multiplex/... -run TestMultiplex_Leaks

# Stress tests
go test -race -tags=stress -timeout 5m ./pkg/multiplex/... -run TestMultiplex_Stress

# Protocol package tests
go test -race ./pkg/multiplex/internal/protocol/... -run TestFrame_EncodeDecode
```

## Development Process

- **Iterate in small steps.** Implement one logical piece at a time. Never move on to the next step until the current step compiles, passes all tests, and you are confident it is correct.
- **Before every commit**, run the full validation sequence in order:
  ```bash
  make fmt
  make lint
  make test
  make test-long
  ```
  All steps must pass. Do not commit with failing tests or lint errors.
- **Commit messages** must be a single line (e.g. `feat: add flow control egress`). No co-author trailers.

## Architecture

### Package Layout

- `pkg/multiplex/multiplex.go` — **All public API lives here**: `Upgrader`, `Dialer`, `Conn`, `Channel`
- `pkg/multiplex/internal/protocol/protocol.go` — Wire format: frame encoding/decoding, flag constants, varint ChannelID
- `cmd/` — Example binaries: `ws-client`, `ws-server`, `ws-rexec-client`, `ws-rexec-server`

### Wire Protocol

Each frame: `[Varint ChannelID][1-byte Flag][Payload]`

Flags: `FlagData(0x01)`, `FlagCreate(0x02)`, `FlagClose(0x03)`, `FlagEOF(0x04)`, `FlagWindowUpdate(0x05)`

Protocol version string used during handshake: `"multiplex.v1.0"`

### Conn Goroutine Model

`Conn` runs three background goroutines:
- `writeLoop()` — Serializes all writes to the underlying WebSocket via an internal `writeCh`. This is necessary because gorilla/websocket is not concurrent-write-safe.
- `readLoop()` — Demultiplexes incoming frames, routing payloads to the correct `Channel.readCh`.
- `pingLoop()` — Sends periodic pings; read deadline is refreshed on each pong.

### Channel State

Each `Channel` has independent half-close state (`localClosed`, `remoteClosed`). `CloseWrite()` sends `FlagEOF` and marks the local side done while leaving the read side open. `Close()` sends `FlagClose` and aborts both directions. The per-channel read buffer is a `chan []byte` with capacity 64.

### Test Organization

Tests in `pkg/multiplex/` use build tags to separate suites:
- No tag: fast unit/integration tests
- `//go:build long`: chaos, leak, heartbeat, fairness, malformed-input, shutdown tests
- `//go:build stress`: high-concurrency data integrity tests

### Active Development: Milestone 6 (Flow Control)

The protocol layer (`FlagWindowUpdate`) is complete. The next steps are implementing `SendWindow` (egress) and `RecvWindow` (ingress) in `Channel` within `multiplex.go`, using `sync.Cond` to block writes when the send window is exhausted. See `PLAN.md` milestones 6.2–6.5.
