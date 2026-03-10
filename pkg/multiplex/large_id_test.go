// Copyright 2023 Sean Sullivan.
// SPDX-License-Identifier: MIT

//go:build long

// This file verifies that the library can handle the full range of uint64 
// for channel IDs across the wire, particularly focusing on Varint 
// encoding edge cases (e.g., 1-byte vs. 10-byte IDs).
package multiplex

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
)

func TestMultiplex_LargeChannelIDs(t *testing.T) {
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
	defer clientConn.Close()
	serverConn := <-serverConnCh
	defer serverConn.Close()

	// List of interesting IDs to test (Varint edge cases)
	ids := []uint64{
		0,
		1,
		127,           // Max 1-byte varint
		128,           // Min 2-byte varint
		16383,         // Max 2-byte varint
		16384,         // Min 3-byte varint
		(1 << 32) - 1, // Max uint32
		1 << 32,
		^uint64(0) - 1,
		^uint64(0), // Max uint64 (10-byte varint)
	}

	for _, id := range ids {
		t.Run(fmt.Sprintf("ID_%d", id), func(t *testing.T) {
			clientCh, err := clientConn.CreateChannel(id)
			if err != nil {
				t.Fatalf("Failed to create channel with ID %d: %v", id, err)
			}

			// Wait for server to perceive the new channel
			serverCh := <-waitForChannel(serverConn)
			if serverCh.GetChannelID() != id {
				t.Errorf("Server received wrong ID. Got %d, want %d", serverCh.GetChannelID(), id)
			}

			// Verify data can flow on this ID
			testData := []byte(fmt.Sprintf("data-for-id-%d", id))
			
			go func() {
				_ = clientCh.WriteMessage(testData)
			}()

			received, err := serverCh.ReadMessage()
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(received, testData) {
				t.Errorf("Data mismatch. Got %s, want %s", received, testData)
			}
			
			// Clean up channel for next subtest
			_ = clientCh.Close()
		})
	}
}
