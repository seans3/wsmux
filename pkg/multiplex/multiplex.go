// Copyright 2023 Sean Sullivan.
// SPDX-License-Identifier: MIT

package multiplex

import (
	"context"
	"io"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"
	"github.com/seans3/websockets/pkg/multiplex/internal/protocol"
)

// Upgrader wraps gorilla.Upgrader to support multiplexed connections.
type Upgrader struct {
	websocket.Upgrader
}

// Upgrade upgrades the HTTP server connection to the multiplexed protocol.
func (u *Upgrader) Upgrade(w http.ResponseWriter, r *http.Request, responseHeader http.Header) (*Conn, error) {
	u.Subprotocols = append(u.Subprotocols, protocol.ProtocolVersion)
	c, err := u.Upgrader.Upgrade(w, r, responseHeader)
	if err != nil {
		return nil, err
	}
	return NewConn(c), nil
}

// Dialer wraps gorilla.Dialer to support multiplexed connections.
type Dialer struct {
	websocket.Dialer
}

// Dial creates a new multiplexed connection to the specified URL.
func (d *Dialer) Dial(ctx context.Context, url string, requestHeader http.Header) (*Conn, *http.Response, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	d.Subprotocols = append(d.Subprotocols, protocol.ProtocolVersion)
	c, resp, err := d.Dialer.DialContext(ctx, url, requestHeader)
	if err != nil {
		return nil, resp, err
	}
	return NewConn(c), resp, nil
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
	
	// done is closed when the connection is terminated
	done chan struct{}
}

// NewConn initializes a new multiplexed connection.
func NewConn(ws *websocket.Conn) *Conn {
	c := &Conn{
		ws:       ws,
		writeCh:  make(chan writeMsg, 256),
		channels: make(map[uint64]*Channel),
		done:     make(chan struct{}),
	}
	go c.writeLoop()
	go c.readLoop()
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
	for {
		select {
		case msg, ok := <-c.writeCh:
			if !ok {
				return
			}
			if err := c.ws.WriteMessage(msg.messageType, msg.data); err != nil {
				c.Close()
				return
			}
		case <-c.done:
			return
		}
	}
}

func (c *Conn) readLoop() {
	defer c.Close()
	for {
		messageType, data, err := c.ws.ReadMessage()
		if err != nil {
			return
		}
		
		if messageType != websocket.BinaryMessage {
			// In our multiplexed protocol, data frames should be binary.
			// We might handle other types (like Close/Ping/Pong) specifically if gorilla doesn't.
			continue
		}

		frame, err := protocol.Decode(data)
		if err != nil {
			continue
		}

		c.handleFrame(frame)
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
		}
		return
	}

	// Channel doesn't exist. Check if it's a create request.
	if f.Flag == protocol.FlagCreate {
		// New inbound channel
		newCh := &Channel{
			id:     f.ChannelID,
			conn:   c,
			readCh: make(chan []byte, 64),
		}
		c.mu.Lock()
		c.channels[f.ChannelID] = newCh
		handler := c.onChannelCreated
		c.mu.Unlock()

		if handler != nil {
			if err := handler(newCh); err != nil {
				newCh.Close()
			}
		}
	}
}

func (c *Conn) Close() error {
	select {
	case <-c.done:
		return nil
	default:
		close(c.done)
		return c.ws.Close()
	}
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
	
	if _, ok := c.channels[id]; ok {
		return nil, nil // Or return error already exists
	}

	ch := &Channel{
		id:     id,
		conn:   c,
		readCh: make(chan []byte, 64),
	}
	c.channels[id] = ch

	// Notify peer of channel creation
	f := &protocol.Frame{
		ChannelID: id,
		Flag:      protocol.FlagCreate,
	}
	c.writeCh <- writeMsg{messageType: websocket.BinaryMessage, data: f.Encode()}

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
	
	closeOnce sync.Once // specifically for closing readCh
	abortOnce sync.Once // specifically for sending FlagClose and aborting
}

// WriteMessage sends a message over the logical channel.
func (ch *Channel) WriteMessage(data []byte) error {
	ch.mu.Lock()
	if ch.localClosed {
		ch.mu.Unlock()
		return io.ErrClosedPipe
	}
	ch.mu.Unlock()

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
	
	ch.closeReadChannel()
	ch.conn.removeChannel(ch.id)
}

func (ch *Channel) markRemoteClosed() {
	ch.mu.Lock()
	ch.remoteClosed = true
	ch.mu.Unlock()
	
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

	select {
	case ch.readCh <- data:
	default:
		// Buffer full
	}
}

// GetChannelID returns the unique identifier for this channel.
func (ch *Channel) GetChannelID() uint64 {
	return ch.id
}
