# WebSockets Multiplexing Library

A robust Go library that extends [gorilla/websocket](https://github.com/gorilla/websocket) to provide a sophisticated multiplexing/demultiplexing interface. It allows multiple independent, logical streams (**Channels**) to coexist over a single persistent physical WebSocket connection, providing TCP-like semantics (including half-close EOF) for logical streams.

## Features

- **Efficient Multiplexing:** Run unlimited logical streams over one physical connection using a lightweight binary protocol.
- **Standard Library Integration:** `multiplex.Channel` implements `io.Reader` and `io.Writer`, enabling seamless use with `io.Copy`, `bufio`, and other standard tools.
- **Robust Lifecycle Management:** Supports graceful handshake negotiation, half-close (EOF) propagation, and abrupt disconnect recovery.
- **High-Performance Concurrency:** Serialized, non-blocking writes via a dedicated background goroutine and per-channel read buffering.
- **Configurable Reliability:** Built-in Ping/Pong heartbeats and configurable I/O deadlines.
- **Per-Channel Flow Control:** Optional window-based back-pressure prevents Head-of-Line blocking and protects slow readers from being overwhelmed.

---

## Architecture & Design

### 1. Framing Protocol
The library uses a custom binary framing format designed for minimal overhead:
`[Varint ChannelID] [1-byte Flag] [Payload]`

- **ChannelID**: Variable-length integer (Base-128) supporting up to 64-bit IDs while remaining space-efficient for small IDs.
- **Flags**:
    - `0x01 (Data)`: Standard data payload.
    - `0x02 (Create)`: Signal to open a new logical channel.
    - `0x03 (Close)`: Immediate abort of a channel.
    - `0x04 (EOF)`: Graceful half-close; no more data will be sent from this side.
    - `0x05 (WindowUpdate)`: Flow control credit grant; 4-byte big-endian increment payload.

### 2. Concurrency Model
WebSocket connections in Gorilla are not thread-safe for concurrent writes. This library solves this by using a **centralized writer goroutine** per connection. All logical channels dispatch messages to a shared internal channel, ensuring strictly serialized access to the underlying WebSocket while allowing application-level writes to remain non-blocking until the global buffer is saturated.

### 3. Lifecycle State Machine
Each channel maintains an independent state machine allowing for "Half-Close" (EOF) support. This means a client can signal it is finished sending data (e.g., closing a file upload) while continuing to read a response from the server.

---

## Getting Started

### Installation
```bash
make build
```
This builds all example binaries into the `bin/` directory.

### Example 1: Basic Echo
A simple demonstration where every message sent by the client is echoed back by the server.

**Start the Server:**
```bash
./bin/ws-server -addr localhost:8080
```

**Start the Client:**
```bash
./bin/ws-client -addr localhost:8080
```

### Example 2: Remote Execution (ws-rexec)
A more advanced example that pipes `STDIN`, `STDOUT`, and `STDERR` to a remote shell (e.g., `bash`) over three independent logical channels.

**Start the Rexec Server:**
```bash
./bin/ws-rexec-server -addr localhost:8081
```

**Run a Command Remotely:**
```bash
# Interactive mode
./bin/ws-rexec-client -addr localhost:8081

# Piped mode
echo "uptime; exit" | ./bin/ws-rexec-client -addr localhost:8081
```

---

## API Documentation

### Connection Establishment

#### Server: `multiplex.Upgrader`
Wraps `websocket.Upgrader` to handle the multiplexing handshake.
```go
upgrader := multiplex.Upgrader{
    Upgrader:          websocket.Upgrader{...},
    PingInterval:      30 * time.Second,
    ReadTimeout:       60 * time.Second,
    EnableFlowControl: true,   // optional; enables per-channel window-based flow control
    InitialWindow:     65536,  // optional; per-channel window in bytes (default 64KB)
}
conn, err := upgrader.Upgrade(w, r, responseHeader)
```

#### Client: `multiplex.Dialer`
Wraps `websocket.Dialer` to connect to a multiplexed server.
```go
dialer := multiplex.Dialer{
    Dialer:            websocket.Dialer{...},
    PingInterval:      30 * time.Second,
    ReadTimeout:       60 * time.Second,
    EnableFlowControl: true,   // must match the server setting
    InitialWindow:     65536,
}
conn, resp, err := dialer.Dial(ctx, url, requestHeader)
```

### Connection Management: `multiplex.Conn`
- **`CreateChannel(id uint64)`**: Creates a new outbound logical channel.
- **`SetChannelCreatedHandler(handler)`**: Registers a callback for incoming remote channels.
- **`Close()`**: Gracefully closes the connection and all associated channels.
- **`Done()`**: Returns a channel that closes when the connection terminates.

### Logical Streams: `multiplex.Channel`
- **`Read(p []byte)` / `Write(p []byte)`**: Standard `io` interfaces.
- **`CloseWrite()`**: Sends an EOF frame. Local side is done writing; remote side will receive `io.EOF`.
- **`GetChannelID()`**: Returns the unique ID for this stream.

---

## Testing & Verification

The project includes three distinct test suites:
1. **Unit Tests:** Fast verification of core logic (`make test`).
2. **Long Tests:** Resource leak analysis, chaos testing, and stability checks (`make test-long`).
3. **Stress Tests:** High-load concurrency and data integrity validation (`make test-stress`).

To run the full suite:
```bash
make test-all
```

## Roadmap

All planned milestones are complete:
- [x] **Flow Control & Back-pressure**: Window-based per-channel flow control implemented behind `EnableFlowControl` feature gate. Eliminates Head-of-Line blocking.
- [x] **Robustness Testing**: Comprehensive suite for large IDs, malformed frames, and flow control protocol violations.
- [x] **Remote Execution Example**: `ws-rexec` multiplexes stdin/stdout/stderr over three independent channels.

See [PLAN.md](PLAN.md) for the detailed implementation roadmap.
