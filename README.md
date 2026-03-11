# wsmux

**wsmux** multiplexes independent logical streams over a single WebSocket connection — giving you TCP-like channels with `io.Reader`/`io.Writer` semantics, window-based flow control, and EOF half-close, without managing multiple connections.

---

## Why wsmux?

Opening a new WebSocket connection for every stream is expensive: each connection requires its own HTTP upgrade handshake, TLS negotiation, authentication round-trip, and OS-level socket. wsmux eliminates that overhead by running unlimited independent **Channels** over one persistent connection, each with its own lifecycle, back-pressure, and EOF signalling.

**If you are building any of the following, wsmux is a natural fit:**

- **Remote execution endpoint** — multiplex stdin, stdout, and stderr of a remote shell over a single authenticated WebSocket. This is the same pattern used by `kubectl exec` and SSH, built in ~100 lines of Go.
- **Parallel request/response streams** — fan out concurrent RPC calls over one persistent connection without serializing them or managing connection pools.
- **Large file or media transfer** — stream data to a slow reader with built-in window-based flow control; the sender blocks automatically rather than growing an unbounded buffer.

---

## Quick Start

```go
// SERVER — upgrade an HTTP request to a multiplexed connection
upgrader := multiplex.Upgrader{
    Upgrader: websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }},
}
conn, err := upgrader.Upgrade(w, r, nil)
if err != nil {
    log.Fatal(err)
}
defer conn.Close()

// Handle channels opened by the client
conn.SetChannelCreatedHandler(func(ch *multiplex.Channel) error {
    go func() {
        io.Copy(ch, ch) // echo: pipe reads back as writes
        ch.CloseWrite() // signal EOF to the client
    }()
    return nil
})

<-conn.Done()
```

```go
// CLIENT — dial and open a logical channel
dialer := multiplex.Dialer{Dialer: websocket.Dialer{}}
conn, _, err := dialer.Dial(ctx, "ws://localhost:8080", nil)
if err != nil {
    log.Fatal(err)
}
defer conn.Close()

ch, err := conn.CreateChannel(1)
if err != nil {
    log.Fatal(err)
}

ch.Write([]byte("hello"))
ch.CloseWrite() // done writing; server will receive io.EOF

buf := make([]byte, 32)
n, _ := ch.Read(buf)   // reads the echoed reply
fmt.Println(string(buf[:n]))
```

`Channel` implements `io.Reader` and `io.Writer`, so it works with `io.Copy`, `bufio.Scanner`, `json.Decoder`, and any other standard library tool without adaptation.

---

## Features

- **Standard `io` interfaces** — every `Channel` is an `io.Reader` and `io.Writer`; drop it into any existing pipeline.
- **Half-close EOF** — `CloseWrite()` signals the end of your upload while keeping the read side open for the server's response. Matches TCP semantics.
- **Window-based flow control** — enable with `EnableFlowControl: true`. Slow readers get back-pressure instead of unbounded buffer growth; eliminates Head-of-Line blocking between channels.
- **`MaxChannels` limit** — prevents channel-exhaustion DoS from untrusted peers. Set it on both sides.
- **Built-in heartbeats** — configurable Ping/Pong with automatic read-deadline refresh keeps connections alive through proxies and NAT.
- **Concurrent-write-safe** — all channels share one underlying connection; the library serializes writes internally so your code never needs a mutex.
- **Zero-cost logging** — silent by default; opt in with any `*slog.Logger`.

---

## Remote Execution Example

The `ws-rexec` example is the clearest demonstration of wsmux in practice. It multiplexes **stdin (ch 0)**, **stdout (ch 1)**, and **stderr (ch 2)** of a remote `bash` process over a single WebSocket connection, with correct EOF propagation on all three streams.

**Client** (abbreviated from `cmd/ws-rexec-client/main.go`):
```go
conn, _, _ := dialer.Dial(ctx, "ws://localhost:8081/rexec", nil)

stdin,  _ := conn.CreateChannel(0)
stdout, _ := conn.CreateChannel(1)
stderr, _ := conn.CreateChannel(2)

go func() { io.Copy(stdin, os.Stdin);  stdin.CloseWrite() }()  // local stdin  → remote bash
go func() { io.Copy(os.Stdout, stdout) }()                      // remote stdout → local stdout
go func() { io.Copy(os.Stderr, stderr) }()                      // remote stderr → local stderr

<-conn.Done()
```

**Server** (abbreviated from `cmd/ws-rexec-server/main.go`):
```go
conn, _ := upgrader.Upgrade(w, r, nil)

conn.SetChannelCreatedHandler(func(ch *multiplex.Channel) error {
    // collect the three channels by ID, then start bash
    ...
    cmd := exec.Command("bash")
    io.Copy(cmdStdin,  stdin)   // mux channel → bash stdin
    io.Copy(stdout, cmdStdout)  // bash stdout  → mux channel
    io.Copy(stderr, cmdStderr)  // bash stderr  → mux channel
    return nil
})
```

**Build and run:**
```bash
make build

./bin/ws-rexec-server -addr localhost:8081

# interactive shell
./bin/ws-rexec-client -addr localhost:8081

# piped command
echo "uptime; exit" | ./bin/ws-rexec-client -addr localhost:8081
```

---

## Installation

```bash
go get github.com/seans3/wsmux/pkg/multiplex
```

To build and run the bundled example binaries:
```bash
make build   # outputs to bin/
```

---

## API Reference

### Connection Establishment

#### Server: `multiplex.Upgrader`
Wraps `websocket.Upgrader` to handle the multiplexing handshake.
```go
upgrader := multiplex.Upgrader{
    Upgrader:          websocket.Upgrader{...},
    PingInterval:      30 * time.Second,
    ReadTimeout:       60 * time.Second,
    EnableFlowControl: true,   // optional; enables per-channel window-based flow control
    InitialWindow:     65536,  // optional; per-channel window in bytes (default 64 KB)
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

> **Note:** `EnableFlowControl` must match on both sides. If one side enables it and the other does not, frames will be misinterpreted and the connection will malfunction.

### Connection Management: `multiplex.Conn`
- **`CreateChannel(id uint64) (*Channel, error)`** — creates a new outbound logical channel. Returns `ErrChannelIDInUse` if the ID is already open, or `ErrTooManyChannels` if `MaxChannels` is exceeded.
- **`SetChannelCreatedHandler(func(*Channel) error)`** — registers a callback invoked for each channel opened by the remote peer.
- **`Close() error`** — gracefully closes the connection and all associated channels.
- **`Done() <-chan struct{}`** — closed when the connection terminates; use to block until shutdown.

### Logical Streams: `multiplex.Channel`
- **`Read(p []byte) (n int, err error)`** — standard `io.Reader`.
- **`Write(p []byte) (n int, err error)`** — standard `io.Writer`. Blocks when the flow control send window is exhausted.
- **`CloseWrite() error`** — sends an EOF frame; the local side can no longer write, but can still read until the remote also closes.
- **`Close() error`** — aborts the channel immediately and notifies the remote peer.
- **`GetChannelID() uint64`** — returns the unique ID for this stream.

### Logging

The library is **silent by default**: if no `Logger` is set, all output is discarded with zero overhead. To enable logging, pass a `*slog.Logger` to `Upgrader` or `Dialer`:

```go
// Route through the application's default logger
upgrader := multiplex.Upgrader{
    Upgrader: websocket.Upgrader{...},
    Logger:   slog.Default(),
}

// Write debug output to stderr for local development
upgrader := multiplex.Upgrader{
    Upgrader: websocket.Upgrader{...},
    Logger:   slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
        Level: slog.LevelDebug,
    })),
}
```

**Log levels:**

| Level | Events |
|---|---|
| `INFO` | Connection established (with config), connection closed |
| `DEBUG` | Channel created (inbound/outbound), EOF sent/received, channel fully closed, send-window blocked, `WindowUpdate` sent/received |
| `WARN` | Read/write errors, protocol violations (malformed frame, zero-increment `WindowUpdate`, duplicate `FlagCreate`, data for unknown channel), flow control buffer overflow |

All log records are pre-annotated with `remote_addr`; channel-scoped records also include `channel_id`.

### Production Checklist

Before deploying to untrusted peers:
- Set `MaxChannels` to bound memory usage per connection (e.g. `256`).
- Enable `EnableFlowControl` to prevent a fast sender from overwhelming a slow reader — and ensure both sides use the same setting.

---

## Architecture & Wire Protocol

*This section is for contributors and the curious. You do not need to understand it to use wsmux.*

### Framing Protocol
Each frame: `[Varint ChannelID][1-byte Flag][Payload]`

- **ChannelID**: Base-128 varint supporting up to 64-bit IDs, space-efficient for small values.
- **Flags**: `0x01 Data` · `0x02 Create` · `0x03 Close` · `0x04 EOF` · `0x05 WindowUpdate` (4-byte big-endian increment payload)

### Concurrency Model
gorilla/websocket is not concurrent-write-safe. wsmux solves this with a **centralized `writeLoop` goroutine** per connection. All channels enqueue frames to a shared `writeCh`; the write loop serializes them onto the underlying socket. Application-level writes remain non-blocking until the global queue (`writeChCapacity = 256` frames) is saturated.

Three goroutines run per `Conn`: `writeLoop`, `readLoop`, `pingLoop`.

### Channel Lifecycle
Each `Channel` tracks independent half-close state (`localClosed`, `remoteClosed`). `CloseWrite()` sends `FlagEOF` and marks the local side done while leaving the read side open. `Close()` sends `FlagClose` and aborts both directions.

---

## Testing & Verification

The project has four test suites organized by scope:

### 1. Unit Tests (`make test`)
Fast verification of core logic — framing, channel lifecycle, flow control mechanics, protocol edge cases. Runs with the race detector.
```bash
make test
```

### 2. Integration Tests (`make test-long`, `make test-stress`)
Located in `test/integration/`. Exercise the full public API against a real in-process WebSocket server.

- **Long tests** (`//go:build long`): resource leak detection, chaos/fault-injection, heartbeat and read-timeout behaviour, graceful and abrupt shutdown, Head-of-Line blocking verification, channel fairness under flood conditions, and malformed-frame resilience.
- **Stress tests** (`//go:build stress`): high-load concurrency across 20+ channels, cross-channel relay, rapid open/close cycles, multiple parallel connections. Every stress test runs twice — with and without flow control.

```bash
make test-long
make test-stress
```

### 3. End-to-End Tests (`make test-e2e`)
Compile and run `ws-file-server` and `ws-file-client` as real OS processes, transfer a 10 MB random file over a live WebSocket connection, and verify byte-for-byte integrity.

| Test | Flow Control | Window | Window Cycles |
|---|---|---|---|
| `NoFlowControl` | off | — | — |
| `FlowControlDefaultWindow` | on | 64 KB | ~160 |
| `FlowControl4KWindow` | on | 4 KB | ~2,560 |

```bash
make test-e2e
```

### Run everything
```bash
make test-all
```

---

## Roadmap

All planned features are implemented and tested:

- **Multiplexing core** — binary framing, varint channel IDs, concurrent read/write
- **Half-close EOF** — `CloseWrite()` with independent per-direction state
- **Flow control** — window-based per-channel back-pressure behind `EnableFlowControl`
- **`MaxChannels` limit** — DoS protection against channel exhaustion
- **Heartbeats** — configurable Ping/Pong with read-deadline refresh
- **Structured logging** — `log/slog` integration, silent by default
- **Remote execution example** — `ws-rexec` multiplexes stdin/stdout/stderr over three channels

See [PLAN.md](PLAN.md) for the detailed implementation history.
