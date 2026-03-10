// Copyright 2023 Sean Sullivan.
// SPDX-License-Identifier: MIT

// This file contains tests for the WebSocket heartbeat mechanism (Ping/Pong)
// and read/write deadlines, ensuring that stale or broken connections are
// detected and closed.
package multiplex

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestMultiplex_ReadTimeout(t *testing.T) {
	// Setup a test server that doesn't send anything
	upgrader := Upgrader{
		Upgrader: websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }},
	}

	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		// Just sit here and wait
		<-c.Done()
	}))
	defer s.Close()

	u := "ws" + strings.TrimPrefix(s.URL, "http")

	// Dial with a very short read timeout and NO pings
	dialer := Dialer{
		Dialer:       websocket.Dialer{},
		PingInterval: 10 * time.Hour, // Effectively no pings
		ReadTimeout:  100 * time.Millisecond,
	}

	clientConn, _, err := dialer.Dial(context.Background(), u, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientConn.Close()

	// Wait for timeout
	start := time.Now()
	<-clientConn.Done()
	duration := time.Since(start)

	if duration > 500*time.Millisecond {
		t.Errorf("Expected connection to time out quickly, but took %v", duration)
	}
}

func TestMultiplex_Heartbeat(t *testing.T) {
	// Setup a test server that handles pings
	upgrader := Upgrader{
		Upgrader: websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }},
	}

	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		<-c.Done()
	}))
	defer s.Close()

	u := "ws" + strings.TrimPrefix(s.URL, "http")

	// Dial with short read timeout and EVEN SHORTER ping interval
	dialer := Dialer{
		Dialer:       websocket.Dialer{},
		PingInterval: 50 * time.Millisecond,
		ReadTimeout:  200 * time.Millisecond,
	}

	clientConn, _, err := dialer.Dial(context.Background(), u, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientConn.Close()

	// Wait for 500ms. If the connection is still alive, heartbeats are working.
	select {
	case <-clientConn.Done():
		t.Fatal("Connection timed out despite heartbeats")
	case <-time.After(500 * time.Millisecond):
		// Success: heartbeats kept it alive
	}
}
