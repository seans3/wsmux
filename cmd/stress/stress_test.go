// Copyright 2023 Sean Sullivan.
// SPDX-License-Identifier: MIT

package stress

import (
	"bytes"
	"context"
	"crypto/rand"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/seans3/websockets/pkg/multiplex"
)

func TestStress(t *testing.T) {
	// Setup a test server that echoes everything back on every channel
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
		
		c.SetChannelCreatedHandler(func(ch *multiplex.Channel) error {
			go func() {
				defer ch.Close()
				_, _ = io.Copy(ch, ch) // Echo back
			}()
			return nil
		})
		
		serverConnCh <- c
	}))
	defer s.Close()

	// Client Dials
	u := "ws" + strings.TrimPrefix(s.URL, "http")
	dialer := multiplex.Dialer{Dialer: websocket.Dialer{}}
	clientConn, _, err := dialer.Dial(context.Background(), u, nil)
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}
	defer clientConn.Close()

	serverConn := <-serverConnCh
	defer serverConn.Close()

	const numChannels = 10
	const dataSize = 1024 * 1024 // 1MB per channel

	var wg sync.WaitGroup
	wg.Add(numChannels)

	for i := 0; i < numChannels; i++ {
		go func(id uint64) {
			defer wg.Done()

			ch, err := clientConn.CreateChannel(id)
			if err != nil {
				t.Errorf("Failed to create channel %d: %v", id, err)
				return
			}
			// We don't defer ch.Close() here because we want to use CloseWrite() 
			// to signal EOF to the echo server and wait for the response to finish.

			// Generate random data
			randomData := make([]byte, dataSize)
			if _, err := rand.Read(randomData); err != nil {
				t.Errorf("Unexpected error reading random data: %v", err)
				return
			}

			// Channel implements io.Reader and io.Writer
			errCh := make(chan error, 1)
			var received bytes.Buffer
			go func() {
				_, err := io.Copy(&received, ch)
				errCh <- err
			}()

			// Write random data
			n, err := ch.Write(randomData)
			if err != nil {
				t.Errorf("Write error on channel %d: %v", id, err)
				return
			}
			if n != dataSize {
				t.Errorf("Short write on channel %d: %d != %d", id, n, dataSize)
				return
			}

			// Signal EOF so io.Copy on both sides can finish
			if err := ch.CloseWrite(); err != nil {
				t.Errorf("CloseWrite error on channel %d: %v", id, err)
				return
			}

			// Wait for echo to complete
			select {
			case err := <-errCh:
				if err != nil && err != io.EOF {
					t.Errorf("Read error on channel %d: %v", id, err)
				}
			case <-time.After(30 * time.Second): // Stress test might take a while
				t.Errorf("Timeout waiting for echo on channel %d", id)
				return
			}

			// Check the random data sent on STDIN was the same returned on STDOUT.
			if !bytes.Equal(randomData, received.Bytes()) {
				t.Errorf("Data mismatch on channel %d. Sent: %d, Received: %d", id, len(randomData), received.Len())
			}
		}(uint64(i + 1))
	}

	wg.Wait()
}
