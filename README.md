# WebSockets

WebSockets library implmenting multiplexed streams on top of connection.

#### Building

```
$ make build
go install cmd/server/ws-server.go
go install cmd/client/ws-client.go
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

#### Design/Interface WebsockerChannels

1. Server - upgrading connection
  ```
  Upgrader.Upgrade(w http.ResponseWriter, r *http.Request, responseHeader http.Header) (*Conn, error)
  ```

2. Client - request upgrade
  ```
  Dialer.Dial(ctx context.Context, url string, requestHeader http.Header) (*Conn, *http.Response, error)
  ```

3. Connection
  ```
  Conn.NextReader() (io.ReadCloser, error)
  Conn.NextWriter() (io.WriteCloser, error)
  Conn.Close()
  Conn.Subprotocol() (string)

  Conn.SetChannelCreatedHandler(func () (*Channel, error) {})
  Conn.CreateChannel(channelID string, headers map[string]string) (*Channel, error)
  Conn.RemoveChannel(ch *Channel) (error)
  ```

4. Channel - logical stream of independent data within Connection
  ```
  ReadMessage() ([]byte, error)
  WriteMessage([]byte) (error)
  GetChannelID() (string)
  GetHeaders() (map[string]string)
  ```

5. Questions

* Where to set read/write deadline
* Where to set ping/pong heartbeat params


#### TODO

1. Neither client nor server is closing cleanly--fix it.
2. Add ping/pong heartbeat into protocol.
3. Refactor common code into `pkg`.
4. Change hard-coded `os.stdin` and `fmt.Printf` into generalized `io.Reader` and `io.Writer`.
5. Add unit tests.
6. Add abstraction for mutiple streams on top of websocket connection.
7. Add sub-protocol version for client/server.
