// Copyright 2026 Sean Sullivan.
// SPDX-License-Identifier: MIT

// This file contains tests for WebSocket subprotocol negotiation edge cases.
package multiplex

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/seans3/wsmux/pkg/multiplex/internal/protocol"
)

// TestNegotiation_Mismatch verifies behavior when the server does not
// agree to the multiplex subprotocol.
func TestNegotiation_Mismatch(t *testing.T) {
	// Server that explicitly ignores subprotocols
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		}
		// Standard upgrade, no subprotocol negotiation
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
	}))
	defer s.Close()

	u := "ws" + strings.TrimPrefix(s.URL, "http")
	dialer := Dialer{Dialer: websocket.Dialer{}}

	conn, resp, err := dialer.Dial(context.Background(), u, nil)
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}
	defer conn.Close()

	if conn.Subprotocol() != "" {
		t.Errorf("Expected empty subprotocol, got %q", conn.Subprotocol())
	}

	if resp.Header.Get("Sec-WebSocket-Protocol") != "" {
		t.Errorf("Expected no subprotocol header in response")
	}

	// Currently, the library allows the connection to proceed even if
	// negotiation failed. This test documents that behavior.
	// Future versions might return an error during Dial/Upgrade if
	// ProtocolVersion is missing.
}

// TestNegotiation_ExplicitSelection verifies that the library respects
// other subprotocols if requested alongside the multiplex one.
func TestNegotiation_ExplicitSelection(t *testing.T) {
	const otherProto = "chat.v2"

	upgrader := Upgrader{
		Upgrader: websocket.Upgrader{
			CheckOrigin:  func(r *http.Request) bool { return true },
			Subprotocols: []string{otherProto},
		},
	}

	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		if conn.Subprotocol() != otherProto {
			t.Errorf("Server expected %q, got %q", otherProto, conn.Subprotocol())
		}
	}))
	defer s.Close()

	u := "ws" + strings.TrimPrefix(s.URL, "http")
	dialer := Dialer{Dialer: websocket.Dialer{
		Subprotocols: []string{otherProto, protocol.ProtocolVersion},
	}}

	conn, _, err := dialer.Dial(context.Background(), u, nil)
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}
	defer conn.Close()

	if conn.Subprotocol() != otherProto {
		t.Errorf("Client expected %q, got %q", otherProto, conn.Subprotocol())
	}
}
