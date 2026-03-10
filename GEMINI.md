# Project: WebSockets Multiplexing Library

This project is a Go library that extends [gorilla/websocket](https://github.com/gorilla/websocket) to provide a multiplexing/demultiplexing interface. It allows multiple independent, logical streams (Channels) to coexist over a single persistent WebSocket connection.

## Project Overview

- **Purpose:** Enable multiple logical streams over one physical WebSocket connection.
- **Main Technologies:** Go (1.21+), `github.com/gorilla/websocket`.
- **Architecture:**
  - `pkg/multiplex/`: Public API including `Upgrader`, `Dialer`, `Conn`, and `Channel`.
  - `pkg/multiplex/internal/protocol/`: Internal framing protocol using variable-length integer (Varint) IDs and 1-byte status flags.
  - `cmd/`: Example client and server implementations.

## Building and Running

The project uses a `Makefile` for standard development tasks.

### Key Commands

- **Build binaries:** `make build` (outputs to `bin/`)
- **Run Unit Tests:** `make test` (runs fast tests in `pkg/`)
- **Run Long Tests:** `make test-long` (includes leak and backpressure tests)
- **Run Stress Tests:** `make stress` (runs high-load data tests)
- **Run All Tests:** `make test-all`
- **Linting:** `make lint` (requires `golangci-lint`)
- **Formatting:** `make fmt`
- **Tidy Dependencies:** `make tidy`

## Development Conventions

- **Concurrency:** All components are designed to be thread-safe. A dedicated writer goroutine handles serialized writes to the underlying WebSocket.
- **Testing:**
  - **Unit Tests:** Mandated for all new features and bug fixes.
  - **Build Tags:** Specialized tests are gated behind build tags:
    - `long`: For resource leak and HoL blocking analysis.
    - `stress`: For high-load and concurrency stress tests.
  - **Race Detection:** Use `go test -race` (included in all `make` test targets).
- **Error Handling:** Errors are handled explicitly and typically wrapped with context.
- **Protocol:** Uses a binary framing format: `[Varint ChannelID][1-byte Flag][Payload]`.
  - Flags: `0x01` (Data), `0x02` (Create), `0x03` (Close), `0x04` (EOF/Half-Close).
- **Documentation:** All exported types and methods should have descriptive comments.
- **Project Plan:** See `PLAN.md` for the current implementation roadmap and milestones.
