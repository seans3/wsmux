# WebSockets Multiplexing Library

A Go library that extends [gorilla/websocket](https://github.com/gorilla/websocket) to provide a robust multiplexing/demultiplexing interface. This allows multiple independent, logical streams (Channels) to coexist over a single persistent WebSocket connection.

## Features

- **Multiplexing:** Run multiple logical streams over one physical connection.
- **Channel Isolation:** Independent headers and data flow for each channel.
- **Familiar API:** Built on top of standard Go `io` interfaces and common WebSocket patterns.
- **Clean Lifecycle:** Managed connection upgrading and dialing with built-in channel handlers.

## Project Status

This project is currently under active development. See [PLAN.md](PLAN.md) for our implementation roadmap and milestones.

## Building

To build the example server and client:

```bash
make build
```

This installs `ws-server` and `ws-client` to your `$GOPATH/bin`.

## Running the Examples

### 1. Start the Server
```bash
ws-server -addr localhost:8080
```

### 2. Remote Execution Example (ws-rexec)

This example demonstrates piping three independent logical channels (STDIN, STDOUT, STDERR) to a remote shell.

#### Start the Rexec Server
```bash
ws-rexec-server -addr localhost:8081
```

#### Start the Rexec Client
```bash
# Connect and run an interactive bash session
./bin/ws-rexec-client -addr localhost:8081

# Or pipe a command directly
echo "ls -la; exit" | ./bin/ws-rexec-client -addr localhost:8081
```

## API Documentation

The library provides a high-level wrapper around `gorilla/websocket` to handle multiplexing.

### 1. Connection Establishment

#### Server: `multiplex.Upgrader`
Wraps `websocket.Upgrader` to handle the multiplexing handshake.
```go
upgrader := multiplex.Upgrader{
    Upgrader: websocket.Upgrader{...},
    PingInterval: 30 * time.Second,
    ReadTimeout:  60 * time.Second,
}
conn, err := upgrader.Upgrade(w, r, responseHeader)
```

#### Client: `multiplex.Dialer`
Wraps `websocket.Dialer` to connect to a multiplexed server.
```go
dialer := multiplex.Dialer{
    Dialer: websocket.Dialer{...},
    PingInterval: 30 * time.Second,
    ReadTimeout:  60 * time.Second,
}
conn, resp, err := dialer.Dial(ctx, url, requestHeader)
```

### 2. Connection Management: `multiplex.Conn`

A `Conn` represents the physical WebSocket connection hosting multiple logical channels.

- **`CreateChannel(id uint64) (*Channel, error)`**: Creates a new outbound logical channel with the given ID.
- **`SetChannelCreatedHandler(handler func(*Channel) error)`**: Registers a callback for when the remote peer creates a new channel.
- **`Close()`**: Gracefully closes the physical connection and all logical channels.
- **`Done() <-chan struct{}`**: Returns a channel that is closed when the connection terminates.

### 3. Logical Streams: `multiplex.Channel`

A `Channel` represents a single logical stream. It implements the standard `io.Reader` and `io.Writer` interfaces.

- **`Read(p []byte) (int, error)`**: Reads data from the channel. Returns `io.EOF` when the remote peer calls `CloseWrite()`.
- **`Write(p []byte) (int, error)`**: Writes data to the channel.
- **`WriteMessage(data []byte)`**: Sends a single discrete message.
- **`ReadMessage() ([]byte, error)`**: Reads a single discrete message.
- **`CloseWrite()`**: Half-closes the channel (sends EOF). You can no longer write, but can still read.
- **`Close()`**: Full-closes the channel immediately.
- **`GetChannelID() uint64`**: Returns the unique ID of the channel.

## Documentation

For a detailed implementation plan and upcoming features, refer to [PLAN.md](PLAN.md).

## TODO

1. Fix connection teardown (client/server clean close).
2. Implement Ping/Pong heartbeats.
3. Refactor core logic into `/pkg`.
4. Decouple examples from `os.Stdin` / `fmt.Printf`.
5. Add comprehensive unit tests.
6. Implement sub-protocol version negotiation.
