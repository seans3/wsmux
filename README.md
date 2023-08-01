# WebSockets

WebSockets library implmenting multiplexed streams on top of connection.

#### Building

```
$ make build
go build -o bin/ws-server cmd/server/main.go;
go build -o bin/ws-client cmd/client/main.go;
mv bin/ws-server "/usr/local/google/home/seans/go/bin"
mv bin/ws-client "/usr/local/google/home/seans/go/bin"
```

#### Running

Server in one terminal

```
$ ws-server
starting websocket server...localhost:8080
...
```

Client in another terminal. Type into terminal. `<ENTER>` sends message, which is echoed.

```
$ ws-client
starting websocket client...localhost:8080
adsfasdfasdf
adsfasdfasdf
23r1cdf
23r1cdf
...
```

#### TODO

1. Neither client nor server is closing cleanly--fix it.
2. Add ping/pong heartbeat into protocol.
3. Refactor common code into `pkg`.
4. Change hard-coded `os.stdin` and `fmt.Printf` into generalized `io.Reader` and `io.Writer`.
5. Add unit tests.
6. Add `Stream` abstraction on top of websocket connection.
7. Add sub-protocol version for client/server.
