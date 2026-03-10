// Copyright 2023 Sean Sullivan.
// SPDX-License-Identifier: MIT

//go:build long

// This file contains tests for protocol violations and malformed input.
// It ensures that the multiplexer is resilient against buggy or malicious 
// peers and does not panic when receiving unexpected data.
package multiplex

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/seans3/websockets/pkg/multiplex/internal/protocol"
)

func TestMultiplex_MalformedInput(t *testing.T) {
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
	
	// We'll use a raw gorilla websocket to send "illegal" frames
	ws, _, err := websocket.DefaultDialer.Dial(u, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()

	serverConn := <-serverConnCh
	defer serverConn.Close()

	t.Run("Unrecognized Flag", func(t *testing.T) {
		// Send a frame with an invalid flag (e.g., 0xFF)
		f := &protocol.Frame{
			ChannelID: 1,
			Flag:      0xFF,
			Payload:   []byte("garbage"),
		}
		err := ws.WriteMessage(websocket.BinaryMessage, f.Encode())
		if err != nil {
			t.Fatal(err)
		}

		// Connection should remain alive (not panic)
		select {
		case <-serverConn.Done():
			t.Error("Server connection closed after unrecognized flag")
		case <-time.After(100 * time.Millisecond):
			// Success
		}
	})

	t.Run("Data for non-existent channel", func(t *testing.T) {
		f := &protocol.Frame{
			ChannelID: 9999, // Never created
			Flag:      protocol.FlagData,
			Payload:   []byte("where am I going?"),
		}
		err := ws.WriteMessage(websocket.BinaryMessage, f.Encode())
		if err != nil {
			t.Fatal(err)
		}

		// Should be silently ignored, connection remains alive
		select {
		case <-serverConn.Done():
			t.Error("Server connection closed after data for missing channel")
		case <-time.After(100 * time.Millisecond):
			// Success
		}
	})

	t.Run("Duplicate Create", func(t *testing.T) {
		// First create normally
		id := uint64(123)
		f1 := &protocol.Frame{ChannelID: id, Flag: protocol.FlagCreate}
		_ = ws.WriteMessage(websocket.BinaryMessage, f1.Encode())

		// Wait for it to be registered
		time.Sleep(50 * time.Millisecond)

		// Send create again for same ID
		_ = ws.WriteMessage(websocket.BinaryMessage, f1.Encode())

		// Should not panic, just ignore or handle gracefully
		select {
		case <-serverConn.Done():
			t.Error("Server connection closed after duplicate create")
		case <-time.After(100 * time.Millisecond):
			// Success
		}
	})

	t.Run("Invalid Varint", func(t *testing.T) {
		// Send 11 bytes with MSB set (invalid varint)
		invalidData := []byte{0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80}
		err := ws.WriteMessage(websocket.BinaryMessage, invalidData)
		if err != nil {
			t.Fatal(err)
		}

		// handleFrame should log/ignore, connection should stay alive
		select {
		case <-serverConn.Done():
			t.Error("Server connection closed after invalid varint")
		case <-time.After(100 * time.Millisecond):
			// Success
		}
	})
	
	t.Run("Frame too short", func(t *testing.T) {
		// Only 1 byte (might be interpreted as partial ID, but not enough for flag)
		err := ws.WriteMessage(websocket.BinaryMessage, []byte{0x01})
		if err != nil {
			t.Fatal(err)
		}

		select {
		case <-serverConn.Done():
			t.Error("Server connection closed after short frame")
		case <-time.After(100 * time.Millisecond):
			// Success
		}
	})
}
