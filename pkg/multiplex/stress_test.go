// Copyright 2026 Sean Sullivan.
// SPDX-License-Identifier: MIT

//go:build stress

// This file provides high-load and edge-case testing for the multiplexing library.
// It ensures that the protocol framing, concurrency management, and lifecycle state
// machines remain robust under heavy use and various interaction patterns.
//
// Every test runs twice: once without flow control and once with flow control
// enabled (default 64KB window), so regressions in either mode are caught.
package multiplex

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// stressCase parameterises a stress test with and without flow control.
type stressCase struct {
	name    string
	upgrade Upgrader
	dial    Dialer
}

func stressCases() []stressCase {
	base := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	return []stressCase{
		{
			name:    "NoFlowControl",
			upgrade: Upgrader{Upgrader: base},
			dial:    Dialer{Dialer: websocket.Dialer{}},
		},
		{
			name:    "FlowControl",
			upgrade: Upgrader{Upgrader: base, EnableFlowControl: true},
			dial:    Dialer{Dialer: websocket.Dialer{}, EnableFlowControl: true},
		},
	}
}

// TestStress_Echo verifies that multiple logical channels can independently
// send and receive large amounts of data concurrently over a single physical
// connection. It specifically tests:
// 1. Thread-safe multiplexing of data frames.
// 2. Variable chunk sizes to stress framing logic.
// 3. Graceful half-close (EOF) propagation using CloseWrite().
func TestStress_Echo(t *testing.T) {
	for _, tc := range stressCases() {
		t.Run(tc.name, func(t *testing.T) {
			serverConnCh := make(chan *Conn, 1)
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				c, err := tc.upgrade.Upgrade(w, r, nil)
				if err != nil {
					return
				}
				c.SetChannelCreatedHandler(func(ch *Channel) error {
					go func() {
						_, _ = io.Copy(ch, ch)
						_ = ch.CloseWrite()
					}()
					return nil
				})
				serverConnCh <- c
			}))
			defer s.Close()

			u := "ws" + strings.TrimPrefix(s.URL, "http")
			clientConn, _, err := tc.dial.Dial(context.Background(), u, nil)
			if err != nil {
				t.Fatalf("Dial failed: %v", err)
			}
			defer clientConn.Close()
			defer (<-serverConnCh).Close()

			const numChannels = 20
			const dataSize = 512 * 1024 // 512KB per channel

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

					randomData := make([]byte, dataSize)
					if _, err := rand.Read(randomData); err != nil {
						t.Errorf("rand.Read: %v", err)
						return
					}

					errCh := make(chan error, 1)
					var received bytes.Buffer
					go func() {
						_, err := io.Copy(&received, ch)
						errCh <- err
					}()

					written := 0
					for written < dataSize {
						chunkSize := 1024 + int(id%16)*1024
						if written+chunkSize > dataSize {
							chunkSize = dataSize - written
						}
						n, err := ch.Write(randomData[written : written+chunkSize])
						if err != nil {
							t.Errorf("Write error on channel %d: %v", id, err)
							return
						}
						written += n
					}

					if err := ch.CloseWrite(); err != nil {
						t.Errorf("CloseWrite error on channel %d: %v", id, err)
						return
					}

					select {
					case err := <-errCh:
						if err != nil && err != io.EOF {
							t.Errorf("Read error on channel %d: %v", id, err)
						}
					case <-time.After(30 * time.Second):
						t.Errorf("Timeout waiting for echo on channel %d", id)
						return
					}

					if !bytes.Equal(randomData, received.Bytes()) {
						t.Errorf("Data mismatch on channel %d", id)
					}
				}(uint64(i + 1))
			}

			wg.Wait()
		})
	}
}

// TestStress_CrossChannel verifies that data can be relayed between different
// logical channels. This simulates a "proxy" or "control plane" pattern where
// one channel acts as an input and another as an output.
func TestStress_CrossChannel(t *testing.T) {
	for _, tc := range stressCases() {
		t.Run(tc.name, func(t *testing.T) {
			serverConnCh := make(chan *Conn, 1)
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				c, err := tc.upgrade.Upgrade(w, r, nil)
				if err != nil {
					return
				}

				var mu sync.Mutex
				channels := make(map[uint64]*Channel)

				c.SetChannelCreatedHandler(func(ch *Channel) error {
					id := ch.GetChannelID()
					mu.Lock()
					channels[id] = ch
					if id%2 == 0 {
						if partner, ok := channels[id+1]; ok {
							go relay(ch, partner)
						}
					} else {
						if partner, ok := channels[id-1]; ok {
							go relay(partner, ch)
						}
					}
					mu.Unlock()
					return nil
				})

				serverConnCh <- c
			}))
			defer s.Close()

			u := "ws" + strings.TrimPrefix(s.URL, "http")
			clientConn, _, err := tc.dial.Dial(context.Background(), u, nil)
			if err != nil {
				t.Fatalf("Dial failed: %v", err)
			}
			defer clientConn.Close()
			defer (<-serverConnCh).Close()

			const numPairs = 10
			const dataSize = 512 * 1024

			var wg sync.WaitGroup
			wg.Add(numPairs)

			for i := 0; i < numPairs; i++ {
				go func(pairID int) {
					defer wg.Done()

					idIn := uint64(pairID * 2)
					idOut := uint64(pairID*2 + 1)

					chIn, err := clientConn.CreateChannel(idIn)
					if err != nil {
						t.Errorf("Failed to create chIn %d: %v", idIn, err)
						return
					}
					chOut, err := clientConn.CreateChannel(idOut)
					if err != nil {
						t.Errorf("Failed to create chOut %d: %v", idOut, err)
						return
					}

					randomData := make([]byte, dataSize)
					if _, err := rand.Read(randomData); err != nil {
						t.Errorf("rand.Read: %v", err)
						return
					}

					errCh := make(chan error, 1)
					var received bytes.Buffer
					go func() {
						_, err := io.Copy(&received, chOut)
						errCh <- err
					}()

					if _, err := chIn.Write(randomData); err != nil {
						t.Errorf("Write error on chIn %d: %v", idIn, err)
						return
					}
					if err := chIn.CloseWrite(); err != nil {
						t.Errorf("CloseWrite error on chIn %d: %v", idIn, err)
						return
					}

					select {
					case err := <-errCh:
						if err != nil && err != io.EOF {
							t.Errorf("Read error on chOut %d: %v", idOut, err)
						}
					case <-time.After(30 * time.Second):
						t.Errorf("Timeout on pair %d", pairID)
						return
					}

					if !bytes.Equal(randomData, received.Bytes()) {
						t.Errorf("Data mismatch on pair %d", pairID)
					}
				}(i)
			}

			wg.Wait()
		})
	}
}

// relay reads from one channel and writes to another, then propagates EOF.
func relay(in, out *Channel) {
	_, _ = io.Copy(out, in)
	_ = out.CloseWrite()
}

// TestStress_RapidLifecycle stresses the internal channel lookup table by
// rapidly opening and immediately closing many channels, ensuring no race
// conditions in channel registration or cleanup.
func TestStress_RapidLifecycle(t *testing.T) {
	for _, tc := range stressCases() {
		t.Run(tc.name, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				c, err := tc.upgrade.Upgrade(w, r, nil)
				if err != nil {
					return
				}
				c.SetChannelCreatedHandler(func(ch *Channel) error {
					return ch.Close()
				})
				<-c.Done()
			}))
			defer s.Close()

			u := "ws" + strings.TrimPrefix(s.URL, "http")
			clientConn, _, err := tc.dial.Dial(context.Background(), u, nil)
			if err != nil {
				t.Fatalf("Dial failed: %v", err)
			}
			defer clientConn.Close()

			const numChannels = 100
			var wg sync.WaitGroup
			wg.Add(numChannels)

			for i := 0; i < numChannels; i++ {
				go func(id uint64) {
					defer wg.Done()
					ch, err := clientConn.CreateChannel(id)
					if err != nil {
						return
					}
					time.Sleep(time.Duration(id%10) * time.Millisecond)
					_ = ch.Close()
				}(uint64(i + 1))
			}

			wg.Wait()
		})
	}
}

// TestStress_ParallelConns verifies that the library handles multiple concurrent
// physical connections, each managing multiple logical channels.
func TestStress_ParallelConns(t *testing.T) {
	for _, tc := range stressCases() {
		t.Run(tc.name, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				c, err := tc.upgrade.Upgrade(w, r, nil)
				if err != nil {
					return
				}
				c.SetChannelCreatedHandler(func(ch *Channel) error {
					go func() {
						_, _ = io.Copy(ch, ch)
						_ = ch.CloseWrite()
					}()
					return nil
				})
				<-c.Done()
			}))
			defer s.Close()

			u := "ws" + strings.TrimPrefix(s.URL, "http")

			const numConns = 5
			const chsPerConn = 10

			var wg sync.WaitGroup
			wg.Add(numConns)

			for i := 0; i < numConns; i++ {
				go func(connID int) {
					defer wg.Done()
					cc, _, err := tc.dial.Dial(context.Background(), u, nil)
					if err != nil {
						t.Errorf("Dial %d failed: %v", connID, err)
						return
					}
					defer cc.Close()

					var innerWg sync.WaitGroup
					innerWg.Add(chsPerConn)
					for j := 0; j < chsPerConn; j++ {
						go func(chID uint64) {
							defer innerWg.Done()
							ch, err := cc.CreateChannel(chID)
							if err != nil {
								return
							}

							data := []byte(fmt.Sprintf("data-from-conn-%d-ch-%d", connID, chID))
							if _, err := ch.Write(data); err != nil {
								return
							}
							_ = ch.CloseWrite()

							res, _ := io.ReadAll(ch)
							if !bytes.Equal(res, data) {
								t.Errorf("Mismatch on conn %d ch %d", connID, chID)
							}
						}(uint64(j + 1))
					}
					innerWg.Wait()
				}(i)
			}

			wg.Wait()
		})
	}
}
