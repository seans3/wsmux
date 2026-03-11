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
- [x] **Milestone 6.1: Protocol Implementation**
    - [x] Update `internal/protocol` with `FlagWindowUpdate` (0x05).
    - [x] Implement 4-byte payload encoding/decoding for window increments.
    - [x] **Test:** Unit tests in `protocol_test.go` for new frame type.
- [x] **Milestone 6.2: Egress Flow Control**
    - [x] Add `EnableFlowControl` feature gate to `Upgrader`, `Dialer`, `Conn`, and `Channel`.
    - [x] Implement `sendWindow` in `Channel.WriteMessage()` using `sync.Cond` to block writes when the window is exhausted.
    - [x] Handle incoming `FlagWindowUpdate` frames in `handleFrame` to replenish `sendWindow`.
    - [x] **Test:** `TestFlowControl_EgressBlocks`, `TestFlowControl_EgressUnblocksOnConnClose`.
- [x] **Milestone 6.3: Ingress Flow Control**
    - [x] Implement `recvConsumed` tracking in `Channel.Read()` and `Channel.ReadMessage()`.
    - [x] Automatically send `FlagWindowUpdate` frames once consumed bytes cross `initialWindow/2`.
    - [x] **Test:** `TestFlowControl_WindowUpdateSent`, `TestFlowControl_ContinuousStream`.
- [x] **Milestone 6.4: Resolution of HoL Blocking**
    - [x] Refactor `enqueueRead` to be non-blocking when flow control is enabled; overflow is a protocol violation.
    - [x] **Test:** `TestVerification_HoLBlocking_GateOff` (confirms HoL still present with gate off); `TestVerification_HoLBlocking_GateOn` (confirms HoL eliminated with gate on — fast reader reaches ~70,000+ messages vs ~150 without flow control).
- [x] **Milestone 6.5: Robustness**
    - [x] Zero-increment `WindowUpdate` closes the connection as a protocol violation.
    - [x] `WindowUpdate` for an unknown channel is silently ignored.
    - [x] Large window grants are handled without integer overflow.
    - [x] **Test:** `TestFlowControl_ZeroIncrementWindow`, `TestFlowControl_UnknownChannelWindowUpdate`, `TestFlowControl_OverflowWindow`.

### Milestone 7: Compelling Example Application
- [x] Design and implement **`ws-rexec`**: A multiplexed remote command runner.
    - [x] Maps `stdin`, `stdout`, and `stderr` to independent logical channels.
    - [x] Demonstrates `CloseWrite()` signaling EOF to a remote shell (e.g. `bash`).
    - [x] Uses `io.Copy` for idiomatic data piping.

### Milestone 8: Structured Leveled Logging
- [x] **Design:** Silent-by-default via `*slog.Logger` field on `Upgrader`/`Dialer`. Nil logger routes to a no-op discard handler with zero overhead.
- [x] **Step 1 — Connection-level (INFO):** `"connection established"` (with config attrs) and `"connection closed"` emitted by `newConnInternal` and `readLoop`.
- [x] **Step 2 — Write/read errors (ERROR/WARN):** `"write error"` in `writeLoop`; `"read error"` in `readLoop`.
- [x] **Step 3 — Channel lifecycle (DEBUG):** `"channel created (outbound/inbound)"`, `"EOF sent (CloseWrite)"`, `"EOF received"`, `"channel aborted"`, `"channel fully closed"`.
- [x] **Step 4 — Flow control events (DEBUG/WARN):** `"send window exhausted, blocking"`, `"WindowUpdate sent"`, `"WindowUpdate received"`, `"read buffer overflow"` (WARN).
- [x] **Step 5 — Protocol/frame errors (WARN):** `"frame decode failure"`, `"frame for unknown channel"`, `"duplicate FlagCreate"`, `"unrecognized frame flag"`, `"protocol violation: malformed or zero-increment WindowUpdate"`.
- [x] **Step 6 — Tests:** `TestLogging_*` suite verifying nil safety, INFO events, DEBUG channel lifecycle, and inbound channel detection. Uses thread-safe `syncBuffer` to avoid data races when reading log output.
- [x] All log records pre-annotated with `remote_addr`; channel-scoped records also include `channel_id`.

### Milestone 9: Code Quality & Hardening
*Identified and resolved during comprehensive project review.*
- [x] **Dead-code fix:** Removed unreachable `if f.Flag == protocol.FlagCreate` branch in `handleFrame` introduced when the early-return guard was added.
- [x] **Handler error visibility:** `onChannelCreated` errors now logged at WARN before closing the channel, instead of being silently discarded.
- [x] **Send window overflow guard:** `addSendWindow` now checks for `int64` overflow before incrementing; closes the connection as a protocol violation (per RFC 7540 §6.9.1).
- [x] **Remove orphaned constructors:** `NewConn` and `NewConnWithConfig` removed — they bypassed subprotocol negotiation, carried no logger, and were untested.
- [x] **DoS protection — `MaxChannels`:** Added `MaxChannels uint32` to `Upgrader` and `Dialer`. Inbound `FlagCreate` frames that exceed the limit close the connection; outbound `CreateChannel` calls return `ErrTooManyChannels`. Zero means unlimited (default). Tested with `TestMultiplex_MaxChannels`.
- [x] **Error wrapping:** `Upgrade` and `Dial` errors wrapped with `fmt.Errorf("multiplex: upgrade/dial: %w", err)` for actionable caller context.

---

## Technical Considerations

### 1. Framing
**Decision:** Use a variable-length integer (Varint) for `ChannelID`.
*Rationale:* Space-efficient for low channel IDs while supporting IDs up to 64-bit.
*Protocol Format (per frame):*
- `ChannelID`: Varint (Base-128)
- `Flag`: 1 byte (0x01 Data, 0x02 Create, 0x03 Close, 0x04 EOF, 0x05 WindowUpdate)
- `Payload`: Remaining data (WindowUpdate carries a 4-byte big-endian increment)

### 2. Concurrency (Writes)
**Decision:** Use a dedicated writer goroutine with a channel-based dispatch.

### 3. Buffering
**Decision:** Each `Channel` has an internal `chan []byte` buffer. `ReadMessage` pulls from this.

### 4. EOF vs Close
**Decision:**
- `CloseWrite()`: Locally done writing. Sends `FlagEOF`. Read still open.
- `Close()`: Immediate abort. Sends `FlagClose`. Both directions closed.

### 5. Flow Control Window Overflow
**Decision:** Per RFC 7540 §6.9.1, if a received `WindowUpdate` would cause `sendWindow` to exceed `math.MaxInt64`, the connection is closed as a protocol violation. `int64` provides a 9.2 EB ceiling that is unreachable in practice, but the check ensures correctness.

### 6. Channel Limit
**Decision:** `MaxChannels` defaults to 0 (unlimited) for backwards compatibility. Any deployment accepting connections from untrusted peers should set an explicit limit to prevent channel-exhaustion DoS. The inbound limit closes the connection; the outbound limit returns `ErrTooManyChannels` to the caller.

### 7. Logging
**Decision:** Silent by default — `Logger: nil` routes to a `slog.NewTextHandler(io.Discard, nil)` no-op, imposing zero runtime cost. Callers opt in by supplying a `*slog.Logger`. This keeps the library usable in environments with no logging infrastructure without any configuration burden.
