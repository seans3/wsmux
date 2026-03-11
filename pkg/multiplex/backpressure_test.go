// Copyright 2026 Sean Sullivan.
// SPDX-License-Identifier: MIT

//go:build long

// This file contains tests to verify Head-of-Line (HoL) blocking behavior with
// and without per-channel flow control enabled.
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

// runHoLTest creates a server that floods two channels and measures how many
// messages the fast reader accumulates in 2 seconds while the slow reader
// stops after slowReadLimit messages. It returns the fast reader's count.
func runHoLTest(t *testing.T, upgrader Upgrader, dialer Dialer, slowReadLimit int) int {
	t.Helper()

	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
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
	clientConn, _, err := dialer.Dial(context.Background(), u, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientConn.Close()

	slowCh, _ := clientConn.CreateChannel(1)
	fastCh, _ := clientConn.CreateChannel(2)

	fastReadCount := 0
	fastReadDone := make(chan struct{})
	go func() {
		defer close(fastReadDone)
		timeout := time.After(2 * time.Second)
		for {
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

	for i := 0; i < slowReadLimit; i++ {
		if _, err := slowCh.ReadMessage(); err != nil {
			t.Fatal(err)
		}
	}

	<-fastReadDone
	return fastReadCount
}

// TestVerification_HoLBlocking_GateOff verifies that HoL blocking is present
// when flow control is disabled. A slow reader on one channel must stall the
// fast reader on another channel on the same connection.
func TestVerification_HoLBlocking_GateOff(t *testing.T) {
	upgrader := Upgrader{
		Upgrader: websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }},
	}
	dialer := Dialer{Dialer: websocket.Dialer{}}

	count := runHoLTest(t, upgrader, dialer, 100)
	t.Logf("Fast reader count (gate off): %d", count)

	// With HoL blocking present the fast reader stalls once readCh fills up.
	// 500 is a safe upper bound for the blocked case over localhost.
	if count > 500 {
		t.Errorf("fast reader was NOT blocked (count=%d); HoL blocking may be gone with gate off", count)
	} else {
		t.Logf("HoL blocking confirmed: fast reader stalled at %d messages", count)
	}
}

// TestVerification_HoLBlocking_GateOn verifies that HoL blocking is eliminated
// when flow control is enabled. The fast reader must not be stalled by a slow
// reader on a different channel.
func TestVerification_HoLBlocking_GateOn(t *testing.T) {
	upgrader := Upgrader{
		Upgrader:          websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }},
		EnableFlowControl: true,
	}
	dialer := Dialer{
		Dialer:            websocket.Dialer{},
		EnableFlowControl: true,
	}

	count := runHoLTest(t, upgrader, dialer, 100)
	t.Logf("Fast reader count (gate on): %d", count)

	// With HoL blocking eliminated the fast reader should accumulate far more
	// than 500 messages over 2 seconds against a localhost server.
	if count <= 500 {
		t.Errorf("fast reader was unexpectedly stalled (count=%d); HoL blocking still present with gate on", count)
	} else {
		t.Logf("HoL blocking eliminated: fast reader reached %d messages", count)
	}
}
