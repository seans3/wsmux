// Copyright 2023 Sean Sullivan.
// SPDX-License-Identifier: MIT

// Package stress provides high-load and edge-case testing for the multiplexing library.
// It ensures that the protocol framing, concurrency management, and lifecycle state
// machines remain robust under heavy use and various interaction patterns.
package stress

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
	"github.com/seans3/websockets/pkg/multiplex"
)

// TestStress_Echo verifies that multiple logical channels can independently 
// send and receive large amounts of data concurrently over a single physical 
// connection. It specifically tests:
// 1. Thread-safe multiplexing of data frames.
// 2. Variable chunk sizes to stress framing logic.
// 3. Graceful half-close (EOF) propagation using CloseWrite().
func TestStress_Echo(t *testing.T) {
	// Setup a test server that echoes everything back on every channel.
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
		
		// For every new channel created by the client, start an echo goroutine.
		c.SetChannelCreatedHandler(func(ch *multiplex.Channel) error {
			go func() {
				// Echo pattern: Read from ch until EOF, then Write back to ch.
				// io.Copy will stop when it receives the EOF frame from the remote peer.
				_, _ = io.Copy(ch, ch)
				// Signal to the client that we are also done writing.
				_ = ch.CloseWrite()
			}()
			return nil
		})
		
		serverConnCh <- c
	}))
	defer s.Close()

	// Connect to the test server.
	u := "ws" + strings.TrimPrefix(s.URL, "http")
	dialer := multiplex.Dialer{Dialer: websocket.Dialer{}}
	clientConn, _, err := dialer.Dial(context.Background(), u, nil)
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}
	defer clientConn.Close()

	serverConn := <-serverConnCh
	defer serverConn.Close()

	const numChannels = 20
	const dataSize = 512 * 1024 // 512KB per channel

	var wg sync.WaitGroup
	wg.Add(numChannels)

	// Launch multiple concurrent workers, each using a unique logical channel.
	for i := 0; i < numChannels; i++ {
		go func(id uint64) {
			defer wg.Done()

			ch, err := clientConn.CreateChannel(id)
			if err != nil {
				t.Errorf("Failed to create channel %d: %v", id, err)
				return
			}

			// Generate random binary data to ensure protocol-neutral transfer.
			randomData := make([]byte, dataSize)
			if _, err := rand.Read(randomData); err != nil {
				t.Errorf("Unexpected error reading random data: %v", err)
				return
			}

			// Start a background reader to collect the echoed data.
			errCh := make(chan error, 1)
			var received bytes.Buffer
			go func() {
				// io.Copy reads until the server sends an EOF frame.
				_, err := io.Copy(&received, ch)
				errCh <- err
			}()

			// Write random data in random-sized chunks to stress the internal 
			// buffer and framing logic.
			written := 0
			for written < dataSize {
				chunkSize := 1024 + (id % 16 * 1024) // variable chunk sizes based on channel ID
				if written+int(chunkSize) > dataSize {
					chunkSize = uint64(dataSize - written)
				}
				n, err := ch.Write(randomData[written : written+int(chunkSize)])
				if err != nil {
					t.Errorf("Write error on channel %d: %v", id, err)
					return
				}
				written += n
			}

			// Signal half-close (EOF) to the server. The client is done sending, 
			// but still needs to receive the remaining echoed data.
			if err := ch.CloseWrite(); err != nil {
				t.Errorf("CloseWrite error on channel %d: %v", id, err)
				return
			}

			// Wait for the background reader to finish receiving the echoed data.
			select {
			case err := <-errCh:
				if err != nil && err != io.EOF {
					t.Errorf("Read error on channel %d: %v", id, err)
				}
			case <-time.After(30 * time.Second):
				t.Errorf("Timeout waiting for echo on channel %d", id)
				return
			}

			// Verify data integrity.
			if !bytes.Equal(randomData, received.Bytes()) {
				t.Errorf("Data mismatch on channel %d", id)
			}
		}(uint64(i + 1))
	}

	wg.Wait()
}

// TestStress_CrossChannel verifies that data can be relayed between different 
// logical channels. This simulates a "proxy" or "control plane" pattern where 
// one channel acts as an input and another as an output.
func TestStress_CrossChannel(t *testing.T) {
	// Setup a server that relays data between pairs of channels: 
	// Even ID (Inbound) -> (Even ID + 1) (Outbound).
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

		var mu sync.Mutex
		channels := make(map[uint64]*multiplex.Channel)

		// Handler to pair up channels as they are created.
		c.SetChannelCreatedHandler(func(ch *multiplex.Channel) error {
			id := ch.GetChannelID()
			mu.Lock()
			channels[id] = ch
			
			if id%2 == 0 {
				// We just received the "Even" channel. Check if the "Odd" partner exists.
				if partner, ok := channels[id+1]; ok {
					go relay(ch, partner)
				}
			} else {
				// We just received the "Odd" channel. Check if the "Even" partner exists.
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
	dialer := multiplex.Dialer{Dialer: websocket.Dialer{}}
	clientConn, _, err := dialer.Dial(context.Background(), u, nil)
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}
	defer clientConn.Close()

	serverConn := <-serverConnCh
	defer serverConn.Close()

	const numPairs = 10
	const dataSize = 512 * 1024

	var wg sync.WaitGroup
	wg.Add(numPairs)

	// Launch workers to handle pairs of channels.
	for i := 0; i < numPairs; i++ {
		go func(pairID int) {
			defer wg.Done()
			
			idIn := uint64(pairID * 2)
			idOut := uint64(pairID*2 + 1)

			// Create the "Inbound" channel where data will be sent.
			chIn, err := clientConn.CreateChannel(idIn)
			if err != nil {
				t.Errorf("Failed to create chIn %d: %v", idIn, err)
				return
			}
			// Create the "Outbound" channel where relayed data will be received.
			chOut, err := clientConn.CreateChannel(idOut)
			if err != nil {
				t.Errorf("Failed to create chOut %d: %v", idOut, err)
				return
			}

			randomData := make([]byte, dataSize)
			if _, err := rand.Read(randomData); err != nil {
				t.Errorf("Rand read error: %v", err)
				return
			}

			// Reader for the relayed data.
			errCh := make(chan error, 1)
			var received bytes.Buffer
			go func() {
				_, err := io.Copy(&received, chOut)
				errCh <- err
			}()

			// Write to the Inbound channel.
			if _, err := chIn.Write(randomData); err != nil {
				t.Errorf("Write error on chIn %d: %v", idIn, err)
				return
			}
			
			// Half-close Inbound to signal that no more data is coming.
			// This should trigger the server relay to also CloseWrite on chOut.
			if err := chIn.CloseWrite(); err != nil {
				t.Errorf("CloseWrite error on chIn %d: %v", idIn, err)
				return
			}

			// Wait for the full relayed payload to be received.
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
}

// relay helper reads from one channel and writes to another, then propagates EOF.
func relay(in, out *multiplex.Channel) {
	// Read from 'in' until EOF, write everything to 'out'.
	_, _ = io.Copy(out, in)
	// Propagate the EOF to the next hop.
	_ = out.CloseWrite()
}

// TestStress_RapidLifecycle stresses the internal channel lookup table (demuxer)
// by rapidly opening and immediately closing many channels. This ensures 
// that there are no race conditions in channel registration or cleanup.
func TestStress_RapidLifecycle(t *testing.T) {
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
			// Immediate server-side abort/close.
			return ch.Close()
		})
		<-c.Done()
	}))
	defer s.Close()

	u := "ws" + strings.TrimPrefix(s.URL, "http")
	dialer := multiplex.Dialer{Dialer: websocket.Dialer{}}
	clientConn, _, err := dialer.Dial(context.Background(), u, nil)
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
			// Introduce a small, random delay before client-side close to 
			// interleave create/close frames.
			time.Sleep(time.Duration(id%10) * time.Millisecond)
			_ = ch.Close()
		}(uint64(i + 1))
	}

	wg.Wait()
}

// TestStress_ParallelConns verifies that the library handles multiple concurrent 
// physical connections, each managing multiple logical channels. This ensures 
// that global state (if any) is correctly isolated and that the library 
// scales with connection count.
func TestStress_ParallelConns(t *testing.T) {
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
				// Echo and half-close.
				_, _ = io.Copy(ch, ch)
				_ = ch.CloseWrite()
			}()
			return nil
		})
		<-c.Done()
	}))
	defer s.Close()

	u := "ws" + strings.TrimPrefix(s.URL, "http")
	dialer := multiplex.Dialer{Dialer: websocket.Dialer{}}

	const numConns = 5
	const chsPerConn = 10
	
	var wg sync.WaitGroup
	wg.Add(numConns)

	for i := 0; i < numConns; i++ {
		go func(connID int) {
			defer wg.Done()
			cc, _, err := dialer.Dial(context.Background(), u, nil)
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
					
					// Simple request-response check.
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
}
