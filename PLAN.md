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

## Milestones

### Milestone 1: Foundation & Package Refactoring
- [x] Create `pkg/multiplex` and `pkg/multiplex/internal/protocol`.
- [x] Define core public types in `pkg/multiplex`.
- [x] Define internal frame structures and serialization in `internal/protocol`.
- [x] Implement basic `Upgrade` and `Dial` wrappers.

### Milestone 2: Multiplexing Protocol (The "Wire" Format)
- [x] Define the frame header format (Fixed-size uint32 ID + Flag).
- [/] Implement the message router (demuxer) in `Conn` that reads from the physical WebSocket and dispatches messages to the correct `Channel`.
- [/] Implement `WriteMessage` in `Channel` that adds the header and writes to the physical WebSocket via a dedicated writer goroutine.

### Milestone 3: Channel Lifecycle & EOF Support
- [x] Implement `FlagEOF` in `internal/protocol`.
- [x] Add `Channel.CloseWrite()` to support half-close (EOF).
- [x] Update `Conn.handleFrame` to support graceful EOF without immediate channel destruction.
- [x] Implement `SetChannelCreatedHandler` (inbound).
- [x] Add `io.EOF` signaling to `Channel.ReadMessage()`.
- [x] Ensure `Conn` only removes channels from the map after bi-directional closure or a `FlagClose` reset.

### Milestone 4: Connection Reliability & Heartbeats
- [ ] Add `PingInterval` and `ReadTimeout` configuration to `Conn`.
- [ ] Implement periodic `Ping` sending in a dedicated goroutine.
- [ ] Implement `Pong` handling and read deadline refreshing.
- [ ] Ensure clean physical shutdown with WebSocket `CloseMessage` handshake.
### Milestone 5: Resilience & Advanced Testing
- [ ] Implement a `FaultInjectedConn` wrapper for `net.Conn` to simulate:
    - Random message drops.
    - Latency spikes.
    - Sudden connection resets.
- [ ] Add Go Fuzz targets for `protocol.Decode` to identify panic-inducing malformed frames.
- [ ] Implement "Stress/Concurrency" tests: 1000+ simultaneous channels with random data sizes.
- [ ] Add integration tests for "Dangling Channels": verify memory is reclaimed if a peer disappears without `FlagClose`.
- [ ] Replace hard-coded `os.Stdin`/`fmt.Printf` in examples with `io.Reader`/`io.Writer`.

## Development Principles
...
8. **Fault Tolerance:** Library must not panic on malformed network input. 
9. **Fuzzing:** Protocol parsing logic must have 100% coverage via `go test -fuzz`.

### Milestone 6: Advanced Protocol Features
- [ ] Implement sub-protocol version negotiation.
- [ ] Add support for flow control (optional/future).

---

## Technical Considerations & Questions

### 1. Framing
**Decision:** Use a variable-length integer (Varint) for `ChannelID`.
*Rationale:* Space-efficient for low channel IDs (1 byte for ID < 128) while supporting IDs up to 64-bit if needed.
*Protocol Format (per frame):*
- `ChannelID`: Varint (Base-128)
- `Flag`: 1 byte (e.g., `0x01` Data, `0x02` Create, `0x03` Close)
- `Payload`: Remaining data

### 2. Handshake & Versioning
**Decision:** Use the standard `Sec-WebSocket-Protocol` header during the `Upgrade` and `Dial` phases to negotiate the multiplexing version (e.g., `multiplex.v1.0`).

### 3. Concurrency (Writes)
**Decision:** Use a dedicated writer goroutine with a channel-based dispatch.
*Rationale:* 
- **Serialization:** Ensures strictly serial access to the underlying `gorilla/websocket` connection as required.
- **Non-blocking:** `Channel.WriteMessage` becomes a non-blocking (or at least less-blocking) operation by sending to the dispatch channel.
- **Performance:** While a `sync.Mutex` has lower overhead for low-contention scenarios, a dedicated writer goroutine scales better under high contention and allows for future optimizations like write-coalescing or message prioritization. It also simplifies connection teardown by allowing the writer to handle the `CloseMessage` as just another queued task.

### 4. Buffering
**Decision:** Each `Channel` will have an internal channel (`chan []byte`) to buffer incoming messages from the demuxer. `Channel.ReadMessage()` will pull from this internal channel.

### 4. Error Propagation
**Decision:** If the physical `Conn` fails, all associated `Channels` must be closed and their pending `ReadMessage` calls should return an error.
