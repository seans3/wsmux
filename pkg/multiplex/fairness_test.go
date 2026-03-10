// Copyright 2023 Sean Sullivan.
// SPDX-License-Identifier: MIT

//go:build long

// This file contains tests to verify fairness between logical channels.
// It ensures that a high-bandwidth "flooder" channel does not completely
// starve a low-latency "interactive" channel on the same connection.
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

const (
	floodChannelID = 1
	pingChannelID  = 2
)

func TestMultiplex_Fairness(t *testing.T) {
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
				id := ch.GetChannelID()
				for {
					msg, err := ch.ReadMessage()
					if err != nil {
						return
					}
					if id == floodChannelID {
						// The flood channel is just drained to keep the
						// connection's readLoop moving.
						continue
					}
					// Echo other channels (like the "ping" channel)
					_ = ch.WriteMessage(msg)
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

	floodCh, _ := clientConn.CreateChannel(floodChannelID)
	pingCh, _ := clientConn.CreateChannel(pingChannelID)

	// Start a "flooder" that tries to saturate the connection
	floodDone := make(chan struct{})
	go func() {
		defer close(floodDone)
		data := make([]byte, 1024)
		timeout := time.After(2 * time.Second)
		for {
			select {
			case <-timeout:
				return
			default:
				if err := floodCh.WriteMessage(data); err != nil {
					return
				}
				// We don't read back on floodCh to avoid blocking
				// on the server side (due to HoL blocking in enqueueRead).
			}
		}
	}()

	// Measure "ping" latency while the flooder is running
	successCount := 0
	pingIterations := 20
	for i := 0; i < pingIterations; i++ {
		start := time.Now()
		err := pingCh.WriteMessage([]byte("ping"))
		if err != nil {
			t.Errorf("Ping write error: %v", err)
			break
		}

		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		msgCh := readMessageAsync(pingCh)
		select {
		case <-ctx.Done():
			t.Logf("Ping %d timed out (flooder might be saturating buffer)", i)
		case _, ok := <-msgCh:
			if ok {
				successCount++
				t.Logf("Ping %d took %v", i, time.Since(start))
			}
		}
		cancel()
		time.Sleep(50 * time.Millisecond)
	}

	<-floodDone

	t.Logf("Fairness results: %d/%d pings succeeded during flood", successCount, pingIterations)

	// In a fair system, we expect most pings to get through eventually,
	// even if the flood channel is aggressive.
	// If the shared writeCh is full, both will block equally.
	if successCount < (pingIterations / 2) {
		t.Errorf("Low-latency channel was starved: only %d/%d pings succeeded", successCount, pingIterations)
	}
}
