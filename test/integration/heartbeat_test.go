// Copyright 2026 Sean Sullivan.
// SPDX-License-Identifier: MIT

//go:build long

// This file contains tests for the WebSocket heartbeat mechanism (Ping/Pong)
// and read/write deadlines, ensuring that stale or broken connections are
// detected and closed.
package integration

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/seans3/websockets/pkg/multiplex"
)

func TestMultiplex_ReadTimeout(t *testing.T) {
	upgrader := multiplex.Upgrader{
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

	dialer := multiplex.Dialer{
		Dialer:       websocket.Dialer{},
		PingInterval: 10 * time.Hour,
		ReadTimeout:  100 * time.Millisecond,
	}

	clientConn, _, err := dialer.Dial(context.Background(), u, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientConn.Close()

	start := time.Now()
	<-clientConn.Done()
	duration := time.Since(start)

	if duration > 500*time.Millisecond {
		t.Errorf("Expected connection to time out quickly, but took %v", duration)
	}
}

func TestMultiplex_Heartbeat(t *testing.T) {
	upgrader := multiplex.Upgrader{
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

	dialer := multiplex.Dialer{
		Dialer:       websocket.Dialer{},
		PingInterval: 50 * time.Millisecond,
		ReadTimeout:  200 * time.Millisecond,
	}

	clientConn, _, err := dialer.Dial(context.Background(), u, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientConn.Close()

	select {
	case <-clientConn.Done():
		t.Fatal("Connection timed out despite heartbeats")
	case <-time.After(500 * time.Millisecond):
		// Success: heartbeats kept it alive
	}
}
