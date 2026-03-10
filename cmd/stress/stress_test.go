// Copyright 2023 Sean Sullivan.
// SPDX-License-Identifier: MIT

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

func TestStress_Echo(t *testing.T) {
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
				// Echo pattern: Read from ch, Write back to ch.
				// io.Copy will stop when it gets EOF from the remote peer (CloseWrite).
				// Then we CloseWrite back to signal we are done too.
				_, _ = io.Copy(ch, ch)
				_ = ch.CloseWrite()
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

			// Write random data in random chunks to stress framing
			written := 0
			for written < dataSize {
				chunkSize := 1024 + (id % 16 * 1024) // variable chunk sizes
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

			// Signal EOF (half-close)
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
}

func TestStress_CrossChannel(t *testing.T) {
	// Server relays Even ID -> (Even ID + 1)
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

		c.SetChannelCreatedHandler(func(ch *multiplex.Channel) error {
			id := ch.GetChannelID()
			mu.Lock()
			channels[id] = ch
			
			if id%2 == 0 {
				// Inbound (Even)
				if partner, ok := channels[id+1]; ok {
					go relay(ch, partner)
				}
			} else {
				// Outbound (Odd)
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
				t.Errorf("Rand read error: %v", err)
				return
			}

			errCh := make(chan error, 1)
			var received bytes.Buffer
			go func() {
				_, err := io.Copy(&received, chOut)
				errCh <- err
			}()

			// Write to Inbound channel
			if _, err := chIn.Write(randomData); err != nil {
				t.Errorf("Write error on chIn %d: %v", idIn, err)
				return
			}
			
			// Half-close Inbound to signal we are done sending
			if err := chIn.CloseWrite(); err != nil {
				t.Errorf("CloseWrite error on chIn %d: %v", idIn, err)
				return
			}

			// Wait for relay to finish on Outbound channel
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

func relay(in, out *multiplex.Channel) {
	// Read from 'in' until EOF, write to 'out'
	_, _ = io.Copy(out, in)
	// Signal EOF on 'out'
	_ = out.CloseWrite()
}

func TestStress_RapidLifecycle(t *testing.T) {
	// Rapidly open and close channels to stress the demuxer and connection state
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
			// Immediate close
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
				// Might fail if connection is closing, but here it should succeed
				return
			}
			// Random delay
			time.Sleep(time.Duration(id%10) * time.Millisecond)
			_ = ch.Close()
		}(uint64(i + 1))
	}

	wg.Wait()
}

func TestStress_ParallelConns(t *testing.T) {
	// Stress with multiple physical connections, each with multiple channels
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
