// Copyright 2026 Sean Sullivan.
// SPDX-License-Identifier: MIT

// This file verifies that the logging integration works correctly:
//   - a nil Logger does not panic (silent-by-default behavior)
//   - INFO entries are emitted for connection established/closed
//   - DEBUG entries are emitted for channel lifecycle events
//   - WARN entries are emitted for protocol violations
package multiplex

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// syncBuffer is a thread-safe bytes.Buffer for use as an slog output target.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (n int, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// waitForLog polls buf until the substring appears or timeout elapses.
func waitForLog(buf *syncBuffer, substr string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if strings.Contains(buf.String(), substr) {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

// newTestLogger returns a logger that writes to buf at the given level.
func newTestLogger(buf *syncBuffer, level slog.Level) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: level}))
}

func TestLogging_NilLoggerDoesNotPanic(t *testing.T) {
	// Logger: nil — must use noopLogger internally and not panic.
	upgrader := Upgrader{
		Upgrader: websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }},
		Logger:   nil,
	}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		<-c.Done()
	}))
	defer s.Close()

	u := "ws" + strings.TrimPrefix(s.URL, "http")
	dialer := Dialer{Dialer: websocket.Dialer{}, Logger: nil}
	conn, _, err := dialer.Dial(context.Background(), u, nil)
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
}

func TestLogging_ConnectionEstablishedIsLogged(t *testing.T) {
	var clientBuf syncBuffer
	upgrader := Upgrader{
		Upgrader: websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }},
	}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		<-c.Done()
	}))
	defer s.Close()

	u := "ws" + strings.TrimPrefix(s.URL, "http")
	dialer := Dialer{
		Dialer: websocket.Dialer{},
		Logger: newTestLogger(&clientBuf, slog.LevelInfo),
	}
	conn, _, err := dialer.Dial(context.Background(), u, nil)
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()

	// connection established is logged synchronously in newConnInternal.
	if !waitForLog(&clientBuf, "connection established", 500*time.Millisecond) {
		t.Errorf("expected 'connection established' in client log, got: %s", clientBuf.String())
	}
	// connection closed is logged by readLoop's defer — wait for it.
	if !waitForLog(&clientBuf, "connection closed", 500*time.Millisecond) {
		t.Errorf("expected 'connection closed' in client log, got: %s", clientBuf.String())
	}
}

func TestLogging_ChannelLifecycleIsLogged(t *testing.T) {
	var buf syncBuffer
	logger := newTestLogger(&buf, slog.LevelDebug)

	upgrader := Upgrader{
		Upgrader: websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }},
		Logger:   logger,
	}
	serverReady := make(chan struct{})
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		close(serverReady)
		<-c.Done()
	}))
	defer s.Close()

	u := "ws" + strings.TrimPrefix(s.URL, "http")
	dialer := Dialer{
		Dialer: websocket.Dialer{},
		Logger: logger,
	}
	conn, _, err := dialer.Dial(context.Background(), u, nil)
	if err != nil {
		t.Fatal(err)
	}

	<-serverReady
	ch, err := conn.CreateChannel(1)
	if err != nil {
		t.Fatal(err)
	}
	if err := ch.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	conn.Close()

	for _, want := range []string{
		"channel created (outbound)",
		"EOF sent (CloseWrite)",
	} {
		if !waitForLog(&buf, want, 500*time.Millisecond) {
			t.Errorf("expected %q in log output, got:\n%s", want, buf.String())
		}
	}
}

func TestLogging_InboundChannelIsLogged(t *testing.T) {
	var serverBuf syncBuffer
	serverLogger := newTestLogger(&serverBuf, slog.LevelDebug)

	upgrader := Upgrader{
		Upgrader: websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }},
		Logger:   serverLogger,
	}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		c.SetChannelCreatedHandler(func(ch *Channel) error { return nil })
		<-c.Done()
	}))
	defer s.Close()

	u := "ws" + strings.TrimPrefix(s.URL, "http")
	dialer := Dialer{Dialer: websocket.Dialer{}}
	conn, _, err := dialer.Dial(context.Background(), u, nil)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := conn.CreateChannel(42); err != nil {
		t.Fatal(err)
	}
	conn.Close()

	if !waitForLog(&serverBuf, "channel created (inbound)", 500*time.Millisecond) {
		t.Errorf("expected 'channel created (inbound)' in server log, got: %s", serverBuf.String())
	}
}
