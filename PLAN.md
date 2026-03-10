# Implementation Plan: WebSockets Multiplexing

This document outlines the roadmap and milestones for implementing the multiplexed WebSocket library.

## Architectural Goal
Wrap `github.com/gorilla/websocket` to provide a `multiplex.Conn` and `multiplex.Channel` interface, enabling multiple logical streams over a single connection. We will use an `internal` directory to hide implementation details (like framing and protocol state) from the public API.

## Directory Structure
- `pkg/multiplex/`: Public API (Upgrader, Dialer, Conn, Channel).
- `pkg/multiplex/internal/protocol/`: Internal framing, header serialization, and constant definitions.

## Development Principles

To ensure high code quality and maintainability, all contributions must adhere to the following:

1.  **Testing First:** All new features and bug fixes MUST have accompanying unit tests.
2.  **Test Coverage:** We aim for at least **80% unit test coverage** for all packages.
3.  **Pre-commit Validation:** Code MUST successfully build, pass all tests, and pass linting (`make lint`) before being committed.
4.  **Error Handling:** Errors must be handled explicitly and wrapped with context using `%w` where appropriate.
5.  **Concurrency Safety:** All components must be thread-safe. Use the race detector (`go test -race`) during validation.
6.  **Public Documentation:** All exported types, functions, and methods must have descriptive comments.
7.  **Surgical Changes:** Keep PRs/CLs focused. Avoid unrelated refactoring or "cleanups" in the same change as a feature or fix.
8.  **Fault Tolerance:** Library must not panic on malformed network input. 
9.  **Fuzzing:** Protocol parsing logic must have 100% coverage via `go test -fuzz`.

## Milestones

### Milestone 1: Foundation & Package Refactoring
- [x] Create `pkg/multiplex` and `pkg/multiplex/internal/protocol`.
- [x] Define core public types in `pkg/multiplex`.
- [x] Define internal frame structures and serialization in `internal/protocol`.
- [x] Implement basic `Upgrade` and `Dial` wrappers.

### Milestone 2: Multiplexing Protocol (The "Wire" Format)
- [x] Define the frame header format (Variable-length Varint ID + Flag).
- [x] Implement the message router (demuxer) in `Conn`.
- [x] Implement thread-safe `WriteMessage` via a dedicated writer goroutine.

### Milestone 3: Channel Lifecycle & EOF Support
- [x] Implement `FlagEOF` for half-close support.
- [x] Add `Channel.CloseWrite()` to signal EOF.
- [x] Implement bi-directional closure state machine.
- [x] Add `io.EOF` signaling to `Channel.ReadMessage()`.

### Milestone 4: Connection Reliability & Heartbeats
- [x] Add `PingInterval` and `ReadTimeout` configuration.
- [x] Implement periodic `Ping` sending.
- [x] Implement `Pong` handling and read deadline refreshing.
- [x] Ensure clean shutdown with WebSocket `CloseMessage` handshake.

### Milestone 5: Resilience & Idiomatic API
- [x] Add Go Fuzz targets for `protocol.Decode`.
- [x] Implement a `FaultInjectedConn` for chaos testing.
- [x] Implement **`io.Reader` and `io.Writer` interfaces** for `Channel`.
- [x] Add integration tests for "Dangling Channels" cleanup (abrupt disconnects).
- [x] Implement comprehensive robustness tests (Large IDs, Malformed frames, Re-entry).
- [x] Verify concurrency fairness between channels.

### Milestone 6: Per-Channel Flow Control (Window-Based)
- [x] Design window-based flow control mechanism.
- [ ] **Milestone 6.1: Protocol Implementation**
    - [ ] Update `internal/protocol` with `FlagWindowUpdate` (0x05).
    - [ ] Implement 4-byte payload encoding/decoding for window increments.
    - [ ] **Test:** Unit tests in `protocol_test.go` for new frame type.
- [ ] **Milestone 6.2: Egress Flow Control**
    - [ ] Implement `SendWindow` in `Channel.Write()`.
    - [ ] Use `sync.Cond` to block writes when the window is zero.
    - [ ] **Test:** Unit test with a mock receiver that doesn't send window updates.
- [ ] **Milestone 6.3: Ingress Flow Control**
    - [ ] Implement `RecvWindow` tracking in `Channel.Read()`.
    - [ ] Automatically send `FlagWindowUpdate` frames when buffer space is freed.
    - [ ] **Test:** "Long" test verifying window update frames are sent after `Read()`.
- [ ] **Milestone 6.4: Resolution of HoL Blocking**
    - [ ] Refactor `enqueueRead` to be non-blocking (or bounded by window).
    - [ ] **Test (Automatic):** Run `TestVerification_HoLBlocking` (should now pass with high message counts).
    - [ ] **Test (Manual):** Use `ws-rexec` to transfer a large file while running interactive commands on another channel.
- [ ] **Milestone 6.5: Robustness**
    - [ ] Handle protocol violations (peer sending more data than allowed).
    - [ ] **Test:** Malformed input test for window-related violations.

### Milestone 7: Compelling Example Application
- [x] Design and implement **`ws-rexec`**: A multiplexed remote command runner.
    - [x] Maps `stdin`, `stdout`, and `stderr` to independent logical channels.
    - [x] Demonstrates `CloseWrite()` signaling EOF to a remote shell (e.g. `bash`).
    - [x] Uses `io.Copy` for idiomatic data piping.

---

## Technical Considerations

### 1. Framing
**Decision:** Use a variable-length integer (Varint) for `ChannelID`.
*Rationale:* Space-efficient for low channel IDs while supporting IDs up to 64-bit.
*Protocol Format (per frame):*
- `ChannelID`: Varint (Base-128)
- `Flag`: 1 byte (0x01 Data, 0x02 Create, 0x03 Close, 0x04 EOF)
- `Payload`: Remaining data

### 2. Concurrency (Writes)
**Decision:** Use a dedicated writer goroutine with a channel-based dispatch.

### 3. Buffering
**Decision:** Each `Channel` has an internal `chan []byte` buffer. `ReadMessage` pulls from this.

### 4. EOF vs Close
**Decision:** 
- `CloseWrite()`: Locally done writing. Sends `FlagEOF`. Read still open.
- `Close()`: Immediate abort. Sends `FlagClose`. Both directions closed.
