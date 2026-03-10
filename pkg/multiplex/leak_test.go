// Copyright 2023 Sean Sullivan.
// SPDX-License-Identifier: MIT

//go:build long

// This file contains resource leak analysis tests. It ensures that goroutines 
// and other resources are correctly reclaimed after connections and logical 
// channels are closed, preventing memory exhaustion in long-running processes.
package multiplex

import (
	"context"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// TestLeak_Goroutines ensures that creating and closing many connections
// and channels does not leak goroutines.
func TestLeak_Goroutines(t *testing.T) {
	// Warm up to stabilize goroutine count
	runtime.GC()
	initialGoroutines := runtime.NumGoroutine()

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
		c.SetChannelCreatedHandler(func(ch *Channel) error {
			go func() {
				// Simple interaction
				msg, _ := ch.ReadMessage()
				_ = ch.WriteMessage(msg)
				_ = ch.Close()
			}()
			return nil
		})
		<-c.Done()
	}))
	defer s.Close()

	u := "ws" + strings.TrimPrefix(s.URL, "http")
	dialer := Dialer{Dialer: websocket.Dialer{}}

	const iterations = 50
	const channelsPerConn = 10

	for i := 0; i < iterations; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		clientConn, _, err := dialer.Dial(ctx, u, nil)
		if err != nil {
			t.Fatalf("Iteration %d: Dial failed: %v", i, err)
		}

		for j := 0; j < channelsPerConn; j++ {
			ch, err := clientConn.CreateChannel(uint64(j + 1))
			if err != nil {
				t.Fatalf("Iteration %d, Channel %d: Create failed: %v", i, j, err)
			}
			_ = ch.WriteMessage([]byte("ping"))
			_, _ = ch.ReadMessage()
			_ = ch.Close()
		}

		_ = clientConn.Close()
		cancel()
		// Small sleep to allow goroutines to exit
		time.Sleep(10 * time.Millisecond)
	}

	// Wait for cleanup
	time.Sleep(500 * time.Millisecond)
	runtime.GC()
	finalGoroutines := runtime.NumGoroutine()

	// We allow a small threshold for background runtime goroutines
	// but generally it should be close to initial.
	if finalGoroutines > initialGoroutines+10 {
		t.Errorf("Potential goroutine leak: started with %d, ended with %d", initialGoroutines, finalGoroutines)
	}
}
