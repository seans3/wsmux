// Copyright 2026 Sean Sullivan.
// SPDX-License-Identifier: MIT

// This file contains tests for the per-channel flow control feature.
// Tests are added incrementally as each step of the implementation is completed.
package multiplex

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
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
