// Copyright 2023 Sean Sullivan.
// SPDX-License-Identifier: MIT

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

// TestVerification_HoLBlocking verifies that a slow reader on one channel
// currently blocks all other channels on the same connection.
// This confirms that per-channel flow control is NOT implemented yet.
func TestVerification_HoLBlocking(t *testing.T) {
	upgrader := Upgrader{
		Upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
	}

	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		
		// The server will just keep writing to both channels as fast as possible
		c.SetChannelCreatedHandler(func(ch *Channel) error {
			go func() {
				data := make([]byte, 1024)
				for {
					if err := ch.WriteMessage(data); err != nil {
						return
					}
				}
			}()
			return nil
		})
		<-c.Done()
	}))
	defer s.Close()

	u := "ws" + strings.TrimPrefix(s.URL, "http")
	dialer := Dialer{Dialer: websocket.Dialer{}}
	clientConn, _, err := dialer.Dial(context.Background(), u, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientConn.Close()

	// Create two channels: one slow, one fast
	slowCh, _ := clientConn.CreateChannel(1)
	fastCh, _ := clientConn.CreateChannel(2)

	// fastReader will try to read as much as possible
	fastReadCount := 0
	fastReadDone := make(chan struct{})
	go func() {
		defer close(fastReadDone)
		timeout := time.After(2 * time.Second)
		for {
			// We use readMessageAsync from multiplex_test.go to avoid blocking this goroutine
			msgCh := readMessageAsync(fastCh)
			select {
			case <-timeout:
				return
			case _, ok := <-msgCh:
				if !ok {
					return
				}
				fastReadCount++
			}
		}
	}()

	// slowReader will stop reading after 100 messages, filling its buffer (size 64)
	// and eventually blocking the whole connection's readLoop.
	slowReadCount := 0
	for i := 0; i < 100; i++ {
		_, err := slowCh.ReadMessage()
		if err != nil {
			t.Fatal(err)
		}
		slowReadCount++
	}

	// Wait for fastReader to time out
	<-fastReadDone

	t.Logf("Fast reader managed to read %d messages while slow reader was blocked", fastReadCount)

	// If HoL blocking is present, fastReadCount will be very small (close to 64 + 1)
	// because the physical connection's readLoop is blocked by the slow channel's enqueueRead.
	// 64 is the internal readCh buffer size.
	if fastReadCount > 200 { 
		// 200 is a safe upper bound; with 1024 byte messages over localhost, 
		// it would be thousands if NOT blocked.
		t.Errorf("Fast reader was NOT blocked by slow reader. Count: %d. This suggests HoL blocking is gone?", fastReadCount)
	} else {
		t.Logf("HoL blocking confirmed: Fast reader stalled at %d messages", fastReadCount)
	}
}
