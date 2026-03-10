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

## API Preview

### Server Upgrader
```go
upgrader := multiplex.Upgrader{...}
conn, err := upgrader.Upgrade(w, r, responseHeader)
```

### Client Dialer
```go
dialer := multiplex.Dialer{...}
conn, _, err := dialer.Dial(ctx, url, requestHeader)
```

### Connection Management
```go
// Create a new logical channel
channel, err := conn.CreateChannel("chat-room-1", map[string]string{"user": "alice"})

// Handle incoming channels from the remote peer
conn.SetChannelCreatedHandler(func(ch *multiplex.Channel) error {
    fmt.Printf("New channel created: %s\n", ch.GetChannelID())
    return nil
})
```

### Channel Operations
```go
// Channels implement message-based Read/Write
err := channel.WriteMessage([]byte("Hello World"))
msg, err := channel.ReadMessage()
```

## Documentation

For a detailed implementation plan and upcoming features, refer to [PLAN.md](PLAN.md).

## TODO

1. Fix connection teardown (client/server clean close).
2. Implement Ping/Pong heartbeats.
3. Refactor core logic into `/pkg`.
4. Decouple examples from `os.Stdin` / `fmt.Printf`.
5. Add comprehensive unit tests.
6. Implement sub-protocol version negotiation.
