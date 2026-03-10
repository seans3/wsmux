// Copyright 2026 Sean Sullivan.
// SPDX-License-Identifier: MIT

//go:build long

// This file contains tests for connection shutdown scenarios, including
// graceful WebSocket closure and abrupt TCP-level disconnects.
package multiplex

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// TestShutdown_Graceful verifies that calling Conn.Close() triggers a
// proper WebSocket Close handshake and terminates the connection cleanly.
func TestShutdown_Graceful(t *testing.T) {
	upgrader := Upgrader{
		Upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
	}

	serverConnCh := make(chan *Conn, 1)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		serverConnCh <- c
	}))
	defer s.Close()

	u := "ws" + strings.TrimPrefix(s.URL, "http")
	dialer := Dialer{Dialer: websocket.Dialer{}}
	clientConn, _, err := dialer.Dial(context.Background(), u, nil)
	if err != nil {
		t.Fatal(err)
	}
	serverConn := <-serverConnCh

	// Close from client side
	if err := clientConn.Close(); err != nil {
		t.Errorf("Client Close failed: %v", err)
	}

	// Verify both sides finish
	select {
	case <-clientConn.Done():
		// Success
	case <-time.After(2 * time.Second):
		t.Error("Timed out waiting for client Conn.Done")
	}

	select {
	case <-serverConn.Done():
		// Success
	case <-time.After(2 * time.Second):
		t.Error("Timed out waiting for server Conn.Done")
	}
}

// connIntercept is a net.Listener that lets us capture the server-side net.Conn
type connIntercept struct {
	net.Listener
	captured chan net.Conn
}

func (l *connIntercept) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err == nil {
		l.captured <- c
	}
	return c, err
}

// TestShutdown_Abrupt verifies that the library correctly handles
// sudden network-level disconnects (e.g., TCP reset) and returns
// appropriate errors to active readers/writers.
func TestShutdown_Abrupt(t *testing.T) {
	captured := make(chan net.Conn, 1)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	intercept := &connIntercept{
		Listener: ln,
		captured: captured,
	}

	upgrader := Upgrader{
		Upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
	}

	serverConnCh := make(chan *Conn, 1)
	s := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		serverConnCh <- c
	}))
	s.Listener = intercept
	s.Start()
	defer s.Close()

	u := "ws" + strings.TrimPrefix(s.URL, "http")
	dialer := Dialer{Dialer: websocket.Dialer{}}
	clientConn, _, err := dialer.Dial(context.Background(), u, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientConn.Close()

	// Wait for the server-side multiplex.Conn to be ready
	select {
	case <-serverConnCh:
	case <-time.After(time.Second):
		t.Fatal("Timeout waiting for server multiplex.Conn")
	}

	// Capture the raw TCP connection on the server side
	var rawServerConn net.Conn
	select {
	case rawServerConn = <-captured:
	case <-time.After(time.Second):
		t.Fatal("Timeout waiting for raw server connection")
	}

	// Create an active channel
	ch, err := clientConn.CreateChannel(1)
	if err != nil {
		t.Fatal(err)
	}

	// Kill the TCP connection abruptly from the server side without a Close frame
	rawServerConn.Close()

	// Active reading should fail with an error, not hang
	errCh := make(chan error, 1)
	go func() {
		_, err := ch.ReadMessage()
		errCh <- err
	}()

	select {
	case err := <-errCh:
		if err == nil {
			t.Error("Expected error from ReadMessage after abrupt shutdown, got nil")
		}
	case <-time.After(5 * time.Second):
		t.Error("ReadMessage hung after abrupt shutdown")
	}

	// Conn.Done should still trigger eventually (via readLoop failing)
	select {
	case <-clientConn.Done():
		// Success
	case <-time.After(5 * time.Second):
		t.Error("clientConn.Done did not trigger after abrupt shutdown")
	}
}
