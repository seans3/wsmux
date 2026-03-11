// Copyright 2026 Sean Sullivan.
// SPDX-License-Identifier: MIT

//go:build long

// This file contains tests for protocol violations and malformed input.
// It ensures that the multiplexer is resilient against buggy or malicious
// peers and does not panic when receiving unexpected data.
package integration

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/seans3/websockets/pkg/multiplex"
)

// Wire-format flag constants mirroring the internal protocol package.
const (
	flagData   byte = 0x01
	flagCreate byte = 0x02
)

func TestMultiplex_MalformedInput(t *testing.T) {
	upgrader := multiplex.Upgrader{
		Upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
	}

	serverConnCh := make(chan *multiplex.Conn, 1)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		serverConnCh <- c
	}))
	defer s.Close()

	u := "ws" + strings.TrimPrefix(s.URL, "http")

	// Use a raw gorilla websocket to send "illegal" frames directly.
	ws, _, err := websocket.DefaultDialer.Dial(u, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()

	serverConn := <-serverConnCh
	defer serverConn.Close()

	t.Run("Unrecognized Flag", func(t *testing.T) {
		err := ws.WriteMessage(websocket.BinaryMessage,
			encodeFrame(1, 0xFF, []byte("garbage")))
		if err != nil {
			t.Fatal(err)
		}
		select {
		case <-serverConn.Done():
			t.Error("Server connection closed after unrecognized flag")
		case <-time.After(100 * time.Millisecond):
			// Success
		}
	})

	t.Run("Data for non-existent channel", func(t *testing.T) {
		err := ws.WriteMessage(websocket.BinaryMessage,
			encodeFrame(9999, flagData, []byte("where am I going?")))
		if err != nil {
			t.Fatal(err)
		}
		select {
		case <-serverConn.Done():
			t.Error("Server connection closed after data for missing channel")
		case <-time.After(100 * time.Millisecond):
			// Success
		}
	})

	t.Run("Duplicate Create", func(t *testing.T) {
		frame := encodeFrame(123, flagCreate, nil)
		_ = ws.WriteMessage(websocket.BinaryMessage, frame)
		time.Sleep(50 * time.Millisecond)
		_ = ws.WriteMessage(websocket.BinaryMessage, frame)
		select {
		case <-serverConn.Done():
			t.Error("Server connection closed after duplicate create")
		case <-time.After(100 * time.Millisecond):
			// Success
		}
	})

	t.Run("Invalid Varint", func(t *testing.T) {
		// 11 bytes with MSB set — not a valid varint.
		invalidData := []byte{0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80}
		err := ws.WriteMessage(websocket.BinaryMessage, invalidData)
		if err != nil {
			t.Fatal(err)
		}
		select {
		case <-serverConn.Done():
			t.Error("Server connection closed after invalid varint")
		case <-time.After(100 * time.Millisecond):
			// Success
		}
	})

	t.Run("Frame too short", func(t *testing.T) {
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
