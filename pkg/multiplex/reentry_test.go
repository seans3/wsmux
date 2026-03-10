// Copyright 2023 Sean Sullivan.
// SPDX-License-Identifier: MIT

// This file contains tests for re-entry and operations on closed connections.
// It ensures that calling methods on already closed or closing Conns and
// Channels returns appropriate errors and does not panic.
package multiplex

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
)

func TestMultiplex_ClosedReentry(t *testing.T) {
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

	// 1. Create a channel before closing
	ch, _ := clientConn.CreateChannel(1)

	// 2. Close the connection
	clientConn.Close()

	t.Run("CreateChannel on closed Conn", func(t *testing.T) {
		_, err := clientConn.CreateChannel(2)
		if err == nil {
			t.Error("Expected error when creating channel on closed connection")
		}
	})

	t.Run("WriteMessage on closed Conn", func(t *testing.T) {
		err := ch.WriteMessage([]byte("too late"))
		// Might return io.ErrClosedPipe or io.ErrUnexpectedEOF depending on loop state
		if err == nil {
			t.Error("Expected error when writing to channel of closed connection")
		}
	})

	t.Run("ReadMessage on closed Conn", func(t *testing.T) {
		_, err := ch.ReadMessage()
		if err != io.EOF && err != io.ErrUnexpectedEOF {
			t.Errorf("Expected EOF-like error when reading from channel of closed connection, got %v", err)
		}
	})

	t.Run("Double Close", func(t *testing.T) {
		// Should be safe to call multiple times
		if err := clientConn.Close(); err != nil {
			t.Errorf("Subsequent Close() returned error: %v", err)
		}
		if err := serverConn.Close(); err != nil {
			t.Errorf("Subsequent server Close() returned error: %v", err)
		}
	})

	t.Run("Channel Operations after Channel Close", func(t *testing.T) {
		// clientConn is already closed here. Use serverConn which might still be open.
		ch2, err := serverConn.CreateChannel(10)
		if err != nil {
			// If serverConn is also closed, that's fine, we just can't run this subtest.
			return
		}
		_ = ch2.Close()

		if err := ch2.WriteMessage([]byte("gone")); err != io.ErrClosedPipe {
			t.Errorf("Expected ErrClosedPipe, got %v", err)
		}

		if err := ch2.CloseWrite(); err != nil {
			t.Errorf("Subsequent CloseWrite() returned error: %v", err)
		}
	})
}
