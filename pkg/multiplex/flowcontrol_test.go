// Copyright 2026 Sean Sullivan.
// SPDX-License-Identifier: MIT

// This file contains tests for the per-channel flow control feature.
// Tests are added incrementally as each step of the implementation is completed.
package multiplex

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/seans3/websockets/pkg/multiplex/internal/protocol"
)

// newFlowControlPair creates a connected client/server pair with flow control enabled
// and the given initial window size. The server echoes all data back on every channel.
func newFlowControlPair(t *testing.T, initialWindow uint32) (clientConn *Conn, serverConn *Conn, cleanup func()) {
	t.Helper()
	upgrader := Upgrader{
		Upgrader:          websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }},
		EnableFlowControl: true,
		InitialWindow:     initialWindow,
	}

	serverConnCh := make(chan *Conn, 1)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("server upgrade: %v", err)
			return
		}
		serverConnCh <- c
		<-c.Done()
	}))

	u := "ws" + strings.TrimPrefix(s.URL, "http")
	dialer := Dialer{
		Dialer:            websocket.Dialer{},
		EnableFlowControl: true,
		InitialWindow:     initialWindow,
	}
	cc, _, err := dialer.Dial(context.Background(), u, nil)
	if err != nil {
		s.Close()
		t.Fatalf("dial: %v", err)
	}

	sc := <-serverConnCh
	return cc, sc, func() {
		cc.Close()
		sc.Close()
		s.Close()
	}
}

// TestFlowControl_GateOn_BasicTransfer confirms that enabling the flow control gate
// does not break normal channel creation and data transfer.
func TestFlowControl_GateOn_BasicTransfer(t *testing.T) {
	clientConn, serverConn, cleanup := newFlowControlPair(t, 0 /* use default */)
	defer cleanup()

	clientCh, err := clientConn.CreateChannel(1)
	if err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}

	var serverCh *Channel
	select {
	case serverCh = <-waitForChannel(serverConn):
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for server channel")
	}

	want := []byte("hello with flow control enabled")
	if err := clientCh.WriteMessage(want); err != nil {
		t.Fatalf("WriteMessage: %v", err)
	}

	select {
	case got := <-readMessageAsync(serverCh):
		if !bytes.Equal(got, want) {
			t.Errorf("got %q, want %q", got, want)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for message at server")
	}
}

// TestFlowControl_EgressBlocks verifies that WriteMessage blocks when the send
// window is exhausted and unblocks exactly when a WindowUpdate is received.
func TestFlowControl_EgressBlocks(t *testing.T) {
	const window = 128

	clientConn, serverConn, cleanup := newFlowControlPair(t, window)
	defer cleanup()

	clientCh, err := clientConn.CreateChannel(1)
	if err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}
	var serverCh *Channel
	select {
	case serverCh = <-waitForChannel(serverConn):
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for server channel")
	}
	_ = serverCh // server side exists; we won't read from it to keep window exhausted

	// First write (100 bytes) fits within the 128-byte window — must not block.
	first := make([]byte, 100)
	if err := clientCh.WriteMessage(first); err != nil {
		t.Fatalf("first WriteMessage failed: %v", err)
	}

	// Second write (100 bytes) would exceed the remaining 28-byte window.
	// It must block. Verify it hasn't returned after a short delay.
	var writeErr atomic.Value
	writeDone := make(chan struct{})
	go func() {
		defer close(writeDone)
		err := clientCh.WriteMessage(make([]byte, 100))
		if err != nil {
			writeErr.Store(err)
		}
	}()

	select {
	case <-writeDone:
		t.Fatal("second WriteMessage returned before window was granted — expected it to block")
	case <-time.After(100 * time.Millisecond):
		// Good: write is still blocked.
	}

	// Send a WindowUpdate from the server side to grant more credit.
	frame := protocol.EncodeWindowUpdate(clientCh.GetChannelID(), 256)
	serverConn.writeCh <- writeMsg{messageType: websocket.BinaryMessage, data: frame.Encode()}

	// The blocked write must now complete promptly.
	select {
	case <-writeDone:
		if v := writeErr.Load(); v != nil {
			t.Fatalf("second WriteMessage returned error: %v", v)
		}
	case <-time.After(time.Second):
		t.Fatal("second WriteMessage did not unblock after WindowUpdate")
	}
}

// TestFlowControl_WindowUpdateSent verifies that the receiver automatically sends
// WindowUpdate frames as it consumes data, allowing a transfer that exceeds the
// initial window to complete without deadlock.
func TestFlowControl_WindowUpdateSent(t *testing.T) {
	// Small window so we cross it quickly: 512 bytes.
	// We will transfer 8KB total — 16x the window — which requires many WindowUpdates.
	const window = 512
	const totalBytes = 8 * 1024

	clientConn, serverConn, cleanup := newFlowControlPair(t, window)
	defer cleanup()

	clientCh, err := clientConn.CreateChannel(1)
	if err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}
	var serverCh *Channel
	select {
	case serverCh = <-waitForChannel(serverConn):
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for server channel")
	}

	// Sender: write totalBytes in 64-byte chunks.
	sendDone := make(chan error, 1)
	go func() {
		chunk := make([]byte, 64)
		for sent := 0; sent < totalBytes; sent += len(chunk) {
			if err := clientCh.WriteMessage(chunk); err != nil {
				sendDone <- err
				return
			}
		}
		sendDone <- clientCh.CloseWrite()
	}()

	// Receiver: read everything and count bytes.
	var received int
	for {
		data, err := serverCh.ReadMessage()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("ReadMessage: %v", err)
		}
		received += len(data)
	}

	if err := <-sendDone; err != nil {
		t.Fatalf("sender error: %v", err)
	}
	if received != totalBytes {
		t.Errorf("received %d bytes, want %d", received, totalBytes)
	}
}

// TestFlowControl_ContinuousStream transfers 1MB over a single channel with the
// default window size to verify correctness at scale.
func TestFlowControl_ContinuousStream(t *testing.T) {
	const totalBytes = 1024 * 1024 // 1MB

	clientConn, serverConn, cleanup := newFlowControlPair(t, 0 /* default 64KB */)
	defer cleanup()

	clientCh, err := clientConn.CreateChannel(1)
	if err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}
	var serverCh *Channel
	select {
	case serverCh = <-waitForChannel(serverConn):
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for server channel")
	}

	sendDone := make(chan error, 1)
	go func() {
		chunk := make([]byte, 4096)
		for sent := 0; sent < totalBytes; sent += len(chunk) {
			if err := clientCh.WriteMessage(chunk); err != nil {
				sendDone <- err
				return
			}
		}
		sendDone <- clientCh.CloseWrite()
	}()

	var received int
	for {
		data, err := serverCh.ReadMessage()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("ReadMessage: %v", err)
		}
		received += len(data)
	}

	if err := <-sendDone; err != nil {
		t.Fatalf("sender error: %v", err)
	}
	if received != totalBytes {
		t.Errorf("received %d bytes, want %d", received, totalBytes)
	}
}

// TestFlowControl_ZeroIncrementWindow verifies that a WindowUpdate frame with a
// zero increment is treated as a protocol violation and closes the connection.
func TestFlowControl_ZeroIncrementWindow(t *testing.T) {
	clientConn, serverConn, cleanup := newFlowControlPair(t, 0)
	defer cleanup()

	clientCh, err := clientConn.CreateChannel(1)
	if err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}
	select {
	case <-waitForChannel(serverConn):
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for server channel")
	}

	// Send a zero-increment WindowUpdate from the server to the client.
	// The client's handleFrame should close the connection on receipt.
	frame := protocol.EncodeWindowUpdate(clientCh.GetChannelID(), 0)
	serverConn.writeCh <- writeMsg{messageType: websocket.BinaryMessage, data: frame.Encode()}

	// The client connection must close promptly.
	select {
	case <-clientConn.Done():
		// Good: connection was terminated.
	case <-time.After(time.Second):
		t.Fatal("client connection did not close after zero-increment WindowUpdate")
	}
}

// TestFlowControl_UnknownChannelWindowUpdate verifies that a WindowUpdate for a
// non-existent channel is silently ignored and the connection continues normally.
func TestFlowControl_UnknownChannelWindowUpdate(t *testing.T) {
	clientConn, serverConn, cleanup := newFlowControlPair(t, 0)
	defer cleanup()

	clientCh, err := clientConn.CreateChannel(1)
	if err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}
	var serverCh *Channel
	select {
	case serverCh = <-waitForChannel(serverConn):
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for server channel")
	}

	// Send a WindowUpdate for channel ID 9999, which doesn't exist.
	frame := protocol.EncodeWindowUpdate(9999, 1024)
	serverConn.writeCh <- writeMsg{messageType: websocket.BinaryMessage, data: frame.Encode()}

	// Give it a moment to be processed.
	time.Sleep(50 * time.Millisecond)

	// The connection must still be alive: a normal message exchange should work.
	want := []byte("still alive")
	if err := clientCh.WriteMessage(want); err != nil {
		t.Fatalf("WriteMessage after unknown WindowUpdate: %v", err)
	}
	select {
	case got := <-readMessageAsync(serverCh):
		if !bytes.Equal(got, want) {
			t.Errorf("got %q, want %q", got, want)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout reading message after unknown-channel WindowUpdate")
	}
}

// TestFlowControl_IoCopySmallWindow transfers 1MB via io.Copy with a 4KB window.
// Before the Write-chunking fix this deadlocked: io.Copy uses 32KB buffers, which
// exceed the 4KB window, so WriteMessage blocked forever waiting for credits that
// could never arrive.
func TestFlowControl_IoCopySmallWindow(t *testing.T) {
	const window = 4096
	const totalBytes = 1024 * 1024 // 1MB

	clientConn, serverConn, cleanup := newFlowControlPair(t, window)
	defer cleanup()

	clientCh, err := clientConn.CreateChannel(1)
	if err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}
	var serverCh *Channel
	select {
	case serverCh = <-waitForChannel(serverConn):
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for server channel")
	}

	src := bytes.Repeat([]byte{0xAB}, totalBytes)

	sendDone := make(chan error, 1)
	go func() {
		_, err := io.Copy(clientCh, bytes.NewReader(src))
		if err == nil {
			err = clientCh.CloseWrite()
		}
		sendDone <- err
	}()

	dst, err := io.ReadAll(serverCh)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}

	if err := <-sendDone; err != nil {
		t.Fatalf("sender error: %v", err)
	}
	if len(dst) != totalBytes {
		t.Errorf("received %d bytes, want %d", len(dst), totalBytes)
	}
	if !bytes.Equal(dst, src) {
		t.Error("received data does not match sent data")
	}
}

// TestFlowControl_IoCopyVerySmallWindow stress-tests chunking with a tiny 128-byte
// window transferring 256KB, exercising many window replenishment cycles.
func TestFlowControl_IoCopyVerySmallWindow(t *testing.T) {
	const window = 128
	const totalBytes = 256 * 1024

	clientConn, serverConn, cleanup := newFlowControlPair(t, window)
	defer cleanup()

	clientCh, err := clientConn.CreateChannel(1)
	if err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}
	var serverCh *Channel
	select {
	case serverCh = <-waitForChannel(serverConn):
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for server channel")
	}

	src := make([]byte, totalBytes)
	for i := range src {
		src[i] = byte(i)
	}

	sendDone := make(chan error, 1)
	go func() {
		_, err := io.Copy(clientCh, bytes.NewReader(src))
		if err == nil {
			err = clientCh.CloseWrite()
		}
		sendDone <- err
	}()

	dst, err := io.ReadAll(serverCh)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}

	if err := <-sendDone; err != nil {
		t.Fatalf("sender error: %v", err)
	}
	if !bytes.Equal(dst, src) {
		t.Errorf("data mismatch: received %d bytes, want %d", len(dst), totalBytes)
	}
}

// TestFlowControl_IoCopyNoFlowControl confirms io.Copy works without flow control
// (no chunking needed — serves as a baseline).
func TestFlowControl_IoCopyNoFlowControl(t *testing.T) {
	const totalBytes = 1024 * 1024 // 1MB

	mux := http.NewServeMux()
	serverConnCh := make(chan *Conn, 1)
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		upgrader := Upgrader{Upgrader: websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}}
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		serverConnCh <- c
		<-c.Done()
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	u := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws"
	d := Dialer{Dialer: websocket.Dialer{}}
	cc, _, err := d.Dial(context.Background(), u, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer cc.Close()
	sc := <-serverConnCh
	defer sc.Close()

	clientCh, err := cc.CreateChannel(1)
	if err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}
	var serverCh *Channel
	select {
	case serverCh = <-waitForChannel(sc):
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for server channel")
	}

	src := bytes.Repeat([]byte{0xCD}, totalBytes)
	sendDone := make(chan error, 1)
	go func() {
		_, err := io.Copy(clientCh, bytes.NewReader(src))
		if err == nil {
			err = clientCh.CloseWrite()
		}
		sendDone <- err
	}()

	dst, err := io.ReadAll(serverCh)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if err := <-sendDone; err != nil {
		t.Fatalf("sender error: %v", err)
	}
	if !bytes.Equal(dst, src) {
		t.Errorf("data mismatch: received %d bytes, want %d", len(dst), totalBytes)
	}
}

// TestFlowControl_OverflowWindow verifies that repeatedly granting large window
// increments does not cause sendWindow to wrap negative, which would deadlock writers.
func TestFlowControl_OverflowWindow(t *testing.T) {
	clientConn, serverConn, cleanup := newFlowControlPair(t, 0)
	defer cleanup()

	clientCh, err := clientConn.CreateChannel(1)
	if err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}
	select {
	case <-waitForChannel(serverConn):
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for server channel")
	}

	// Grant an enormous window increment several times.
	for i := 0; i < 5; i++ {
		frame := protocol.EncodeWindowUpdate(clientCh.GetChannelID(), ^uint32(0)) // math.MaxUint32
		serverConn.writeCh <- writeMsg{messageType: websocket.BinaryMessage, data: frame.Encode()}
	}
	time.Sleep(50 * time.Millisecond)

	// sendWindow must be positive (not wrapped negative). Verify by writing successfully.
	if err := clientCh.WriteMessage([]byte("post-overflow write")); err != nil {
		t.Fatalf("WriteMessage failed after large window grants: %v", err)
	}
}

// TestFlowControl_EgressUnblocksOnConnClose verifies that a write blocked on the
// send window returns an error when the connection is closed.
func TestFlowControl_EgressUnblocksOnConnClose(t *testing.T) {
	const window = 64

	clientConn, serverConn, cleanup := newFlowControlPair(t, window)
	defer cleanup()

	clientCh, err := clientConn.CreateChannel(1)
	if err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}
	select {
	case <-waitForChannel(serverConn):
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for server channel")
	}

	// Exhaust the window.
	if err := clientCh.WriteMessage(make([]byte, window)); err != nil {
		t.Fatalf("WriteMessage: %v", err)
	}

	// Start a write that will block on the exhausted window.
	writeDone := make(chan error, 1)
	go func() {
		writeDone <- clientCh.WriteMessage(make([]byte, 1))
	}()

	select {
	case <-writeDone:
		t.Fatal("WriteMessage returned before connection was closed")
	case <-time.After(50 * time.Millisecond):
		// Good: still blocked.
	}

	// Close the connection; the blocked write must unblock with an error.
	clientConn.Close()

	select {
	case err := <-writeDone:
		if err == nil {
			t.Fatal("expected an error after connection close, got nil")
		}
	case <-time.After(time.Second):
		t.Fatal("WriteMessage did not unblock after connection close")
	}
}
