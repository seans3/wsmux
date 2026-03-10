// Copyright 2023 Sean Sullivan.
// SPDX-License-Identifier: MIT

// This file verifies the half-close (EOF) and full-close state machine for
// logical channels, ensuring that data can continue to be read after the
// local side has finished writing.
package multiplex

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestMultiplex_HalfClose(t *testing.T) {
	// Setup a test server
	var upgrader = Upgrader{
		Upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
	}

	serverConnCh := make(chan *Conn, 1)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("Upgrade failed: %v", err)
			return
		}
		serverConnCh <- c
	}))
	defer s.Close()

	// Client Dials
	u := "ws" + strings.TrimPrefix(s.URL, "http")
	dialer := Dialer{Dialer: websocket.Dialer{}}
	clientConn, _, err := dialer.Dial(context.Background(), u, nil)
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}
	defer clientConn.Close()

	serverConn := <-serverConnCh
	defer serverConn.Close()

	const channelID = 200
	clientCh, err := clientConn.CreateChannel(channelID)
	if err != nil {
		t.Fatal(err)
	}

	var serverCh *Channel
	select {
	case serverCh = <-waitForChannel(serverConn):
	case <-time.After(time.Second):
		t.Fatal("Timeout waiting for server channel")
	}

	// 1. Client sends half-close
	if err := clientCh.CloseWrite(); err != nil {
		t.Fatal(err)
	}

	// 2. Server should see EOF
	_, err = serverCh.ReadMessage()
	if err != io.EOF {
		t.Errorf("Expected io.EOF, got %v", err)
	}

	// 3. Server can STILL write to client
	testData := []byte("final words")
	if err := serverCh.WriteMessage(testData); err != nil {
		t.Fatal(err)
	}

	// 4. Client should receive the data
	received, err := clientCh.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(received, testData) {
		t.Errorf("Expected %s, got %s", testData, received)
	}

	// 5. Server sends half-close
	if err := serverCh.CloseWrite(); err != nil {
		t.Fatal(err)
	}

	// 6. Client should see EOF
	_, err = clientCh.ReadMessage()
	if err != io.EOF {
		t.Errorf("Expected io.EOF, got %v", err)
	}

	// 7. Verify channels are removed from Conn maps
	time.Sleep(100 * time.Millisecond) // Give a moment for cleanup

	clientConn.mu.RLock()
	if len(clientConn.channels) != 0 {
		t.Errorf("Expected 0 channels in client, got %d", len(clientConn.channels))
	}
	clientConn.mu.RUnlock()

	serverConn.mu.RLock()
	if len(serverConn.channels) != 0 {
		t.Errorf("Expected 0 channels in server, got %d", len(serverConn.channels))
	}
	serverConn.mu.RUnlock()
}
