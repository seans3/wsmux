// Copyright 2026 Sean Sullivan.
// SPDX-License-Identifier: MIT

package multiplex

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/seans3/websockets/pkg/multiplex/internal/protocol"
)

const (
	defaultPingInterval  = 30 * time.Second
	defaultReadTimeout   = 60 * time.Second
	defaultWriteTimeout  = 10 * time.Second
	defaultInitialWindow = uint32(65536) // 64KB

	// writeChCapacity is the number of outbound frames that can be queued
	// before the centralized writer goroutine applies back-pressure.
	writeChCapacity = 256

	// readChCapacity is the per-channel inbound message buffer size when flow
	// control is disabled.
	readChCapacity = 64

	// flowControlReadChMinCapacity and flowControlReadChMaxCapacity bound the
	// per-channel inbound buffer when flow control is enabled. The byte window
	// is the primary constraint; these limits prevent over-allocation for tiny
	// windows and excessive allocation for very large ones.
	flowControlReadChMinCapacity = 256
	flowControlReadChMaxCapacity = 4096

	// windowUpdateThresholdDivisor controls when the receiver sends a
	// WindowUpdate. An update is sent once consumed bytes reach
	// initialWindow / windowUpdateThresholdDivisor.
	windowUpdateThresholdDivisor = 2
)

var (
	ErrChannelIDInUse = errors.New("multiplex: channel ID already in use")
)

// Upgrader wraps gorilla.Upgrader to support multiplexed connections.
type Upgrader struct {
	websocket.Upgrader
	PingInterval      time.Duration
	ReadTimeout       time.Duration
	EnableFlowControl bool
	// InitialWindow sets the per-channel flow control window in bytes.
	// Only used when EnableFlowControl is true. Defaults to 64KB.
	InitialWindow uint32
	// Logger is used for structured leveled logging. If nil, all logging is
	// suppressed. Use slog.Default() to route through the application logger.
	Logger *slog.Logger
}

// Upgrade upgrades the HTTP server connection to the multiplexed protocol.
func (u *Upgrader) Upgrade(w http.ResponseWriter, r *http.Request, responseHeader http.Header) (*Conn, error) {
	uCopy := u.Upgrader
	uCopy.Subprotocols = append([]string(nil), u.Subprotocols...)
	uCopy.Subprotocols = append(uCopy.Subprotocols, protocol.ProtocolVersion)

	c, err := uCopy.Upgrade(w, r, responseHeader)
	if err != nil {
		return nil, err
	}
	return newConnInternal(c, connConfig{
		pingInterval:  u.PingInterval,
		readTimeout:   u.ReadTimeout,
		flowControl:   u.EnableFlowControl,
		initialWindow: u.InitialWindow,
		logger:        u.Logger,
	}), nil
}

// Dialer wraps gorilla.Dialer to support multiplexed connections.
type Dialer struct {
	websocket.Dialer
	PingInterval      time.Duration
	ReadTimeout       time.Duration
	EnableFlowControl bool
	// InitialWindow sets the per-channel flow control window in bytes.
	// Only used when EnableFlowControl is true. Defaults to 64KB.
	InitialWindow uint32
	// Logger is used for structured leveled logging. If nil, all logging is
	// suppressed. Use slog.Default() to route through the application logger.
	Logger *slog.Logger
}

// Dial creates a new multiplexed connection to the specified URL.
func (d *Dialer) Dial(ctx context.Context, url string, requestHeader http.Header) (*Conn, *http.Response, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	dCopy := d.Dialer
	dCopy.Subprotocols = append([]string(nil), d.Subprotocols...)
	dCopy.Subprotocols = append(dCopy.Subprotocols, protocol.ProtocolVersion)

	c, resp, err := dCopy.DialContext(ctx, url, requestHeader)
	if err != nil {
		return nil, resp, err
	}
	return newConnInternal(c, connConfig{
		pingInterval:  d.PingInterval,
		readTimeout:   d.ReadTimeout,
		flowControl:   d.EnableFlowControl,
		initialWindow: d.InitialWindow,
		logger:        d.Logger,
	}), resp, nil
}

// connConfig holds internal configuration for a Conn.
type connConfig struct {
	pingInterval  time.Duration
	readTimeout   time.Duration
	flowControl   bool
	initialWindow uint32
	logger        *slog.Logger
}

// writeMsg represents a message to be written to the physical websocket.
type writeMsg struct {
	messageType int
	data        []byte
}

// Conn represents a multiplexed websocket connection.
type Conn struct {
	ws       *websocket.Conn
	writeCh  chan writeMsg
	channels map[uint64]*Channel
	mu       sync.RWMutex

	onChannelCreated func(*Channel) error

	pingInterval  time.Duration
	readTimeout   time.Duration
	flowControl   bool
	initialWindow uint32

	logger *slog.Logger // never nil after construction

	// done is closed when the connection is terminated
	done      chan struct{}
	closeOnce sync.Once
}

// NewConn initializes a new multiplexed connection with default settings.
func NewConn(ws *websocket.Conn) *Conn {
	return newConnInternal(ws, connConfig{})
}

// NewConnWithConfig initializes a new multiplexed connection with specific timeouts.
func NewConnWithConfig(ws *websocket.Conn, pingInterval, readTimeout time.Duration) *Conn {
	return newConnInternal(ws, connConfig{pingInterval: pingInterval, readTimeout: readTimeout})
}

// noopLogger is a logger that discards all output. Used when no logger is configured.
var noopLogger = slog.New(slog.NewTextHandler(io.Discard, nil))

func newConnInternal(ws *websocket.Conn, cfg connConfig) *Conn {
	if cfg.pingInterval == 0 {
		cfg.pingInterval = defaultPingInterval
	}
	if cfg.readTimeout == 0 {
		cfg.readTimeout = defaultReadTimeout
	}
	if cfg.flowControl && cfg.initialWindow == 0 {
		cfg.initialWindow = defaultInitialWindow
	}
	if cfg.logger == nil {
		cfg.logger = noopLogger
	}

	c := &Conn{
		ws:            ws,
		writeCh:       make(chan writeMsg, writeChCapacity),
		channels:      make(map[uint64]*Channel),
		pingInterval:  cfg.pingInterval,
		readTimeout:   cfg.readTimeout,
		flowControl:   cfg.flowControl,
		initialWindow: cfg.initialWindow,
		logger:        cfg.logger.With("remote_addr", ws.RemoteAddr()),
		done:          make(chan struct{}),
	}

	_ = c.ws.SetReadDeadline(time.Now().Add(c.readTimeout))
	c.ws.SetPongHandler(func(string) error {
		_ = c.ws.SetReadDeadline(time.Now().Add(c.readTimeout))
		return nil
	})

	c.logger.Info("connection established",
		"flow_control", cfg.flowControl,
		"initial_window", cfg.initialWindow,
		"ping_interval", cfg.pingInterval,
		"read_timeout", cfg.readTimeout,
	)

	go c.writeLoop()
	go c.readLoop()
	go c.pingLoop()
	return c
}

// Subprotocol returns the negotiated subprotocol for the connection.
func (c *Conn) Subprotocol() string {
	return c.ws.Subprotocol()
}

// Done returns a channel that is closed when the connection is terminated.
func (c *Conn) Done() <-chan struct{} {
	return c.done
}

func (c *Conn) writeLoop() {
	defer c.ws.Close()
	for {
		select {
		case msg, ok := <-c.writeCh:
			if !ok {
				return
			}
			_ = c.ws.SetWriteDeadline(time.Now().Add(defaultWriteTimeout))
			if err := c.ws.WriteMessage(msg.messageType, msg.data); err != nil {
				c.logger.Error("write error", "err", err)
				return
			}
		case <-c.done:
			// Drain remaining messages briefly
			for {
				select {
				case msg := <-c.writeCh:
					_ = c.ws.SetWriteDeadline(time.Now().Add(defaultWriteTimeout))
					_ = c.ws.WriteMessage(msg.messageType, msg.data)
				default:
					return
				}
			}
		}
	}
}

func (c *Conn) readLoop() {
	defer c.Close()
	defer c.logger.Info("connection closed")
	for {
		messageType, data, err := c.ws.ReadMessage()
		if err != nil {
			c.logger.Warn("read error", "err", err)
			return
		}

		// Refresh read deadline on any activity
		_ = c.ws.SetReadDeadline(time.Now().Add(c.readTimeout))

		if messageType != websocket.BinaryMessage {
			continue
		}

		frame, err := protocol.Decode(data)
		if err != nil {
			continue
		}

		c.handleFrame(frame)
	}
}

func (c *Conn) pingLoop() {
	ticker := time.NewTicker(c.pingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			select {
			case c.writeCh <- writeMsg{messageType: websocket.PingMessage}:
			case <-c.done:
				return
			}
		case <-c.done:
			return
		}
	}
}

func (c *Conn) handleFrame(f *protocol.Frame) {
	c.mu.RLock()
	ch, ok := c.channels[f.ChannelID]
	c.mu.RUnlock()

	if ok {
		switch f.Flag {
		case protocol.FlagData:
			ch.enqueueRead(f.Payload)
		case protocol.FlagEOF:
			ch.markRemoteClosed()
		case protocol.FlagClose:
			ch.abort()
		case protocol.FlagWindowUpdate:
			increment, err := protocol.DecodeWindowUpdate(f.Payload)
			if err != nil || increment == 0 {
				// Malformed or zero-increment window update is a protocol violation.
				c.Close()
				return
			}
			ch.addSendWindow(increment)
		}
		return
	}

	// Channel doesn't exist. Check if it's a create request.
	if f.Flag == protocol.FlagCreate {
		// New inbound channel
		newCh := newChannel(f.ChannelID, c)
		c.mu.Lock()
		c.channels[f.ChannelID] = newCh
		handler := c.onChannelCreated
		c.mu.Unlock()

		c.logger.Debug("channel created (inbound)", "channel_id", f.ChannelID)

		if handler != nil {
			if err := handler(newCh); err != nil {
				newCh.Close()
			}
		}
	}
}

func (c *Conn) Close() error {
	c.closeOnce.Do(func() {
		// Send WebSocket CloseMessage gracefully via the write loop
		select {
		case c.writeCh <- writeMsg{
			messageType: websocket.CloseMessage,
			data:        websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
		}:
		default:
		}
		// Close done to stop ping loop and signal write loop to drain
		close(c.done)
		// Wake all writers that may be blocked on send window.
		c.mu.RLock()
		for _, ch := range c.channels {
			ch.sendCond.Broadcast()
		}
		c.mu.RUnlock()
	})
	return nil
}

func (c *Conn) removeChannel(id uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.channels, id)
}

// SetChannelCreatedHandler sets the callback for new logical channels created
// by the remote peer.
func (c *Conn) SetChannelCreatedHandler(h func(*Channel) error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onChannelCreated = h
}

// CreateChannel creates a new outbound logical channel.
func (c *Conn) CreateChannel(id uint64) (*Channel, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	select {
	case <-c.done:
		return nil, io.ErrClosedPipe
	default:
	}

	if _, ok := c.channels[id]; ok {
		return nil, ErrChannelIDInUse
	}

	ch := newChannel(id, c)
	c.channels[id] = ch

	// Notify peer of channel creation
	f := &protocol.Frame{
		ChannelID: id,
		Flag:      protocol.FlagCreate,
	}
	c.writeCh <- writeMsg{messageType: websocket.BinaryMessage, data: f.Encode()}

	c.logger.Debug("channel created (outbound)", "channel_id", id)
	return ch, nil
}

// Channel represents a logical stream within a Conn.
type Channel struct {
	id     uint64
	conn   *Conn
	readCh chan []byte

	mu           sync.Mutex
	localClosed  bool
	remoteClosed bool

	remainingBuf []byte // To store overflow from Read() calls

	closeOnce sync.Once // specifically for closing readCh
	abortOnce sync.Once // specifically for sending FlagClose and aborting

	flowControl   bool
	initialWindow uint32

	logger *slog.Logger // never nil; inherits remote_addr + channel_id from conn

	// Egress flow control: sendWindow tracks how many bytes we may still send.
	// Writes block on sendCond when sendWindow is exhausted.
	sendMu     sync.Mutex
	sendCond   *sync.Cond
	sendWindow int64

	// Ingress flow control: recvConsumed accumulates bytes consumed by Read/ReadMessage.
	// When it crosses initialWindow/2, a WindowUpdate is sent to replenish the peer's window.
	recvConsumed int64
}

// calcReadChCapacity returns the readCh buffer size for a channel.
// With flow control the byte window is the primary bound, so we use a larger
// message-level buffer to prevent false protocol-violation closures for small
// messages. Without flow control the original capacity is preserved.
func calcReadChCapacity(flowControl bool, initialWindow uint32) int {
	if !flowControl {
		return readChCapacity
	}
	// Allow at least one message per byte of window (very conservative lower
	// bound), floored at flowControlReadChMinCapacity and capped at
	// flowControlReadChMaxCapacity to keep memory reasonable.
	n := int(initialWindow)
	if n < flowControlReadChMinCapacity {
		n = flowControlReadChMinCapacity
	}
	if n > flowControlReadChMaxCapacity {
		n = flowControlReadChMaxCapacity
	}
	return n
}

func newChannel(id uint64, c *Conn) *Channel {
	ch := &Channel{
		id:            id,
		conn:          c,
		readCh:        make(chan []byte, calcReadChCapacity(c.flowControl, c.initialWindow)),
		flowControl:   c.flowControl,
		initialWindow: c.initialWindow,
		logger:        c.logger.With("channel_id", id),
	}
	ch.sendCond = sync.NewCond(&ch.sendMu)
	if c.flowControl {
		ch.sendWindow = int64(c.initialWindow)
	}
	return ch
}

// Read implements io.Reader.
func (ch *Channel) Read(p []byte) (n int, err error) {
	ch.mu.Lock()
	if len(ch.remainingBuf) > 0 {
		n = copy(p, ch.remainingBuf)
		ch.remainingBuf = ch.remainingBuf[n:]
		ch.mu.Unlock()
		atomic.AddInt64(&ch.recvConsumed, int64(n))
		ch.tryFlushRecvWindow()
		return n, nil
	}
	ch.mu.Unlock()

	// ReadMessage already accounts for recvConsumed on the full message.
	data, err := ch.ReadMessage()
	if err != nil {
		return 0, err
	}

	n = copy(p, data)
	if n < len(data) {
		ch.mu.Lock()
		ch.remainingBuf = data[n:]
		ch.mu.Unlock()
	}
	return n, nil
}

// Write implements io.Writer. With flow control enabled, large writes are
// chunked to at most initialWindow bytes to avoid deadlock: a write larger
// than the window blocks forever because the peer can only send credits after
// reading data we haven't sent yet.
func (ch *Channel) Write(p []byte) (n int, err error) {
	if !ch.flowControl {
		if err = ch.WriteMessage(p); err != nil {
			return 0, err
		}
		return len(p), nil
	}
	chunkSize := int(ch.initialWindow)
	if chunkSize == 0 {
		chunkSize = int(defaultInitialWindow)
	}
	for len(p) > 0 {
		end := chunkSize
		if end > len(p) {
			end = len(p)
		}
		if err = ch.WriteMessage(p[:end]); err != nil {
			return n, err
		}
		n += end
		p = p[end:]
	}
	return n, nil
}

// addSendWindow increases the egress send window by n bytes and wakes any
// blocked writers. Called when a FlagWindowUpdate frame is received from the peer.
func (ch *Channel) addSendWindow(n uint32) {
	ch.sendMu.Lock()
	ch.sendWindow += int64(n)
	ch.sendMu.Unlock()
	ch.sendCond.Broadcast()
}

// tryFlushRecvWindow checks whether enough bytes have been consumed to warrant
// sending a WindowUpdate to the peer. It fires once consumed bytes cross half
// the initial window, then resets the counter.
func (ch *Channel) tryFlushRecvWindow() {
	if !ch.flowControl {
		return
	}
	threshold := int64(ch.initialWindow) / windowUpdateThresholdDivisor
	for {
		consumed := atomic.LoadInt64(&ch.recvConsumed)
		if consumed < threshold {
			return
		}
		if atomic.CompareAndSwapInt64(&ch.recvConsumed, consumed, 0) {
			frame := protocol.EncodeWindowUpdate(ch.id, uint32(consumed))
			select {
			case ch.conn.writeCh <- writeMsg{messageType: websocket.BinaryMessage, data: frame.Encode()}:
			case <-ch.conn.done:
			}
			return
		}
	}
}

// WriteMessage sends a message over the logical channel.
func (ch *Channel) WriteMessage(data []byte) error {
	ch.mu.Lock()
	if ch.localClosed {
		ch.mu.Unlock()
		return io.ErrClosedPipe
	}
	ch.mu.Unlock()

	select {
	case <-ch.conn.done:
		return io.ErrClosedPipe
	default:
	}

	// Egress flow control: block until there is enough send window.
	if ch.flowControl {
		ch.sendMu.Lock()
		for ch.sendWindow < int64(len(data)) {
			// Check if the connection was closed while we were waiting.
			select {
			case <-ch.conn.done:
				ch.sendMu.Unlock()
				return io.ErrClosedPipe
			default:
			}
			ch.sendCond.Wait()
		}
		ch.sendWindow -= int64(len(data))
		ch.sendMu.Unlock()
	}

	f := &protocol.Frame{
		ChannelID: ch.id,
		Flag:      protocol.FlagData,
		Payload:   data,
	}

	select {
	case ch.conn.writeCh <- writeMsg{messageType: websocket.BinaryMessage, data: f.Encode()}:
		return nil
	case <-ch.conn.done:
		return io.ErrUnexpectedEOF
	}
}

// ReadMessage blocks until a message is received on the logical channel or
// the channel/connection is closed.
func (ch *Channel) ReadMessage() ([]byte, error) {
	select {
	case data, ok := <-ch.readCh:
		if !ok {
			return nil, io.EOF
		}
		atomic.AddInt64(&ch.recvConsumed, int64(len(data)))
		ch.tryFlushRecvWindow()
		return data, nil
	case <-ch.conn.done:
		return nil, io.ErrUnexpectedEOF
	}
}

// CloseWrite signals EOF to the remote peer. The local channel can no longer
// be used for writing, but it can still receive messages.
func (ch *Channel) CloseWrite() error {
	ch.mu.Lock()
	if ch.localClosed {
		ch.mu.Unlock()
		return nil
	}
	ch.localClosed = true
	ch.mu.Unlock()

	f := &protocol.Frame{
		ChannelID: ch.id,
		Flag:      protocol.FlagEOF,
	}

	select {
	case ch.conn.writeCh <- writeMsg{messageType: websocket.BinaryMessage, data: f.Encode()}:
		ch.logger.Debug("EOF sent (CloseWrite)")
		ch.maybeCleanup()
		return nil
	case <-ch.conn.done:
		return io.ErrUnexpectedEOF
	}
}

// Close aborts the channel immediately and notifies the remote peer.
func (ch *Channel) Close() error {
	ch.abortOnce.Do(func() {
		f := &protocol.Frame{
			ChannelID: ch.id,
			Flag:      protocol.FlagClose,
		}
		select {
		case ch.conn.writeCh <- writeMsg{messageType: websocket.BinaryMessage, data: f.Encode()}:
		case <-ch.conn.done:
		}
		ch.abort()
	})
	return nil
}

func (ch *Channel) abort() {
	ch.mu.Lock()
	ch.localClosed = true
	ch.remoteClosed = true
	ch.mu.Unlock()

	ch.logger.Debug("channel aborted")
	// Wake any writer blocked on the send window so it can observe the closed state.
	ch.sendCond.Broadcast()
	ch.closeReadChannel()
	ch.conn.removeChannel(ch.id)
}

func (ch *Channel) markRemoteClosed() {
	ch.mu.Lock()
	ch.remoteClosed = true
	ch.mu.Unlock()

	ch.logger.Debug("EOF received")
	ch.closeReadChannel()
	ch.maybeCleanup()
}

func (ch *Channel) closeReadChannel() {
	ch.closeOnce.Do(func() {
		close(ch.readCh)
	})
}

func (ch *Channel) maybeCleanup() {
	ch.mu.Lock()
	done := ch.localClosed && ch.remoteClosed
	ch.mu.Unlock()

	if done {
		ch.logger.Debug("channel fully closed")
		ch.conn.removeChannel(ch.id)
	}
}

func (ch *Channel) enqueueRead(data []byte) {
	ch.mu.Lock()
	closed := ch.remoteClosed
	ch.mu.Unlock()
	if closed {
		return
	}

	if ch.flowControl {
		// With flow control the sender must respect the receiver's window, so
		// readCh should never be full. Overflow means the peer violated the
		// protocol; close the connection rather than blocking the readLoop.
		select {
		case ch.readCh <- data:
		default:
			ch.conn.Close()
		}
		return
	}

	select {
	case ch.readCh <- data:
	case <-ch.conn.done:
		// Connection closed, stop trying to deliver
	}
}

// GetChannelID returns the unique identifier for this channel.
func (ch *Channel) GetChannelID() uint64 {
	return ch.id
}
