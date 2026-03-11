# WebSockets Multiplexing Library

A robust Go library that extends [gorilla/websocket](https://github.com/gorilla/websocket) to provide a sophisticated multiplexing/demultiplexing interface. It allows multiple independent, logical streams (**Channels**) to coexist over a single persistent physical WebSocket connection, providing TCP-like semantics (including half-close EOF) for logical streams.

## Features

- **Efficient Multiplexing:** Run unlimited logical streams over one physical connection using a lightweight binary protocol.
- **Standard Library Integration:** `multiplex.Channel` implements `io.Reader` and `io.Writer`, enabling seamless use with `io.Copy`, `bufio`, and other standard tools.
- **Robust Lifecycle Management:** Supports graceful handshake negotiation, half-close (EOF) propagation, and abrupt disconnect recovery.
- **High-Performance Concurrency:** Serialized, non-blocking writes via a dedicated background goroutine and per-channel read buffering.
- **Configurable Reliability:** Built-in Ping/Pong heartbeats and configurable I/O deadlines.
- **Per-Channel Flow Control:** Optional window-based back-pressure prevents Head-of-Line blocking and protects slow readers from being overwhelmed.
- **Structured Leveled Logging:** Optional `log/slog` integration. Silent by default; opt-in by supplying a logger. Zero overhead when disabled.

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
    MaxChannels:       256,    // optional; max concurrent channels (0 = unlimited)
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
    MaxChannels:       256,    // optional; enforced on outbound CreateChannel calls
}
conn, resp, err := dialer.Dial(ctx, url, requestHeader)
```

### Logging

The library is **silent by default**: if no `Logger` is set, all log output is discarded. To enable logging, pass a `*slog.Logger` to the `Upgrader` or `Dialer`:

```go
// Route through the application's default logger
upgrader := multiplex.Upgrader{
    Upgrader: websocket.Upgrader{...},
    Logger:   slog.Default(),
}

// Or write debug output to stderr for local development
upgrader := multiplex.Upgrader{
    Upgrader: websocket.Upgrader{...},
    Logger:   slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
        Level: slog.LevelDebug,
    })),
}
```

**Log levels and what they mean:**

| Level | Events |
|---|---|
| `INFO` | Connection established (with config), connection closed |
| `DEBUG` | Channel created (inbound/outbound), EOF sent/received, channel fully closed, send-window blocked, `WindowUpdate` sent/received |
| `WARN` | Read/write errors, protocol violations (malformed frame, zero-increment `WindowUpdate`, duplicate `FlagCreate`, data for unknown channel), flow control buffer overflow |

All log records are pre-annotated with `remote_addr`; channel-scoped records also include `channel_id`.

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

The project has four test suites organized by scope and execution time:

### 1. Unit Tests (`make test`)
Fast verification of core logic — framing, channel lifecycle, flow control mechanics, protocol edge cases. Runs with the race detector. No build tag required.
```bash
make test
```

### 2. Integration Tests (`make test-long`, `make test-stress`)
Located in `test/integration/`. These tests exercise the full public API against a real in-process WebSocket server.

- **Long tests** (`//go:build long`): Resource leak detection, chaos/fault-injection testing, heartbeat and read-timeout behaviour, graceful and abrupt shutdown, Head-of-Line blocking verification (with and without flow control), channel fairness under flood conditions, and malformed-frame resilience.
- **Stress tests** (`//go:build stress`): High-load concurrency across 20+ channels, cross-channel relay, rapid channel open/close cycles, and multiple parallel connections. Every stress test runs twice — once without flow control and once with — to catch regressions in both modes.

```bash
make test-long    # runs pkg/multiplex and test/integration (long tag)
make test-stress  # runs test/integration (stress tag)
```

### 3. End-to-End Tests (`make test-e2e`)
Located in `test/e2e/`. These tests compile and run the `ws-file-server` and `ws-file-client` binaries as real OS processes, transfer a 10 MB random file over a live WebSocket connection, and verify the received bytes are identical to what was sent. Three scenarios are covered:

| Test | Flow Control | Window | Window Cycles |
|---|---|---|---|
| `NoFlowControl` | off | — | — |
| `FlowControlDefaultWindow` | on | 64 KB (default) | ~160 |
| `FlowControl4KWindow` | on | 4 KB | ~2,560 |

```bash
make test-e2e
```

### Run everything
```bash
make test-all
```

## Roadmap

All planned milestones are complete:
- [x] **Flow Control & Back-pressure**: Window-based per-channel flow control implemented behind `EnableFlowControl` feature gate. Eliminates Head-of-Line blocking.
- [x] **Robustness Testing**: Comprehensive suite for large IDs, malformed frames, and flow control protocol violations.
- [x] **Remote Execution Example**: `ws-rexec` multiplexes stdin/stdout/stderr over three independent channels.

See [PLAN.md](PLAN.md) for the detailed implementation roadmap.
