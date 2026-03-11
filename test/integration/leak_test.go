// Copyright 2026 Sean Sullivan.
// SPDX-License-Identifier: MIT

//go:build long

// This file contains resource leak analysis tests. It ensures that goroutines
// and other resources are correctly reclaimed after connections and logical
// channels are closed, preventing memory exhaustion in long-running processes.
package integration

import (
	"context"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/seans3/wsmux/pkg/multiplex"
)

// TestLeak_Goroutines ensures that creating and closing many connections
// and channels does not leak goroutines.
func TestLeak_Goroutines(t *testing.T) {
	// Warm up to stabilize goroutine count
	runtime.GC()
	initialGoroutines := runtime.NumGoroutine()

	upgrader := multiplex.Upgrader{
		Upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
	}

	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		c.SetChannelCreatedHandler(func(ch *multiplex.Channel) error {
			go func() {
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
	dialer := multiplex.Dialer{Dialer: websocket.Dialer{}}

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
		time.Sleep(10 * time.Millisecond)
	}

	time.Sleep(500 * time.Millisecond)
	runtime.GC()
	finalGoroutines := runtime.NumGoroutine()

	if finalGoroutines > initialGoroutines+10 {
		t.Errorf("Potential goroutine leak: started with %d, ended with %d", initialGoroutines, finalGoroutines)
	}
}
