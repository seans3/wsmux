// Copyright 2026 Sean Sullivan.
// SPDX-License-Identifier: MIT

//go:build long

// This file contains "chaos" tests that use a fault-injected network
// connection to simulate network instability, drops, and latency,
// ensuring the multiplexer can handle ungraceful disconnects.
package integration

import (
	"bytes"
	"context"
	"fmt"
	"math/rand"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/seans3/websockets/pkg/multiplex"
)

// FaultInjectedConn wraps a net.Conn to simulate network issues.
type FaultInjectedConn struct {
	net.Conn
	dropRate float64
	r        *rand.Rand
}

func NewFaultInjectedConn(c net.Conn, dropRate float64) *FaultInjectedConn {
	return &FaultInjectedConn{
		Conn:     c,
		dropRate: dropRate,
		r:        rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

func (c *FaultInjectedConn) Write(b []byte) (n int, err error) {
	if c.r.Float64() < c.dropRate {
		return len(b), nil
	}
	return c.Conn.Write(b)
}

func (c *FaultInjectedConn) Read(b []byte) (n int, err error) {
	return c.Conn.Read(b)
}

func TestMultiplex_ChaosStress(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping stress test in short mode")
	}

	upgrader := multiplex.Upgrader{
		Upgrader: websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }},
	}

	serverConnCh := make(chan *multiplex.Conn, 1)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		serverConnCh <- c
	}))
	defer s.Close()

	u := "ws" + strings.TrimPrefix(s.URL, "http")
	dialer := multiplex.Dialer{Dialer: websocket.Dialer{}}
	clientConn, _, err := dialer.Dial(context.Background(), u, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientConn.Close()
	serverConn := <-serverConnCh
	defer serverConn.Close()

	const numChannels = 50
	const msgsPerChannel = 20
	var wg sync.WaitGroup

	serverConn.SetChannelCreatedHandler(func(ch *multiplex.Channel) error {
		wg.Add(1)
		go func(c *multiplex.Channel) {
			defer wg.Done()
			for {
				msg, err := c.ReadMessage()
				if err != nil {
					return
				}
				_ = c.WriteMessage(msg)
			}
		}(ch)
		return nil
	})

	for i := uint64(1); i <= numChannels; i++ {
		wg.Add(1)
		go func(id uint64) {
			defer wg.Done()
			ch, err := clientConn.CreateChannel(id)
			if err != nil {
				t.Errorf("failed to create channel %d: %v", id, err)
				return
			}
			for j := 0; j < msgsPerChannel; j++ {
				data := []byte(fmt.Sprintf("msg-%d-%d", id, j))
				if err := ch.WriteMessage(data); err != nil {
					return
				}

				ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
				readDone := make(chan struct{})
				var resp []byte
				var readErr error
				go func() {
					resp, readErr = ch.ReadMessage()
					close(readDone)
				}()

				select {
				case <-readDone:
					cancel()
					if readErr == nil && !bytes.Equal(resp, data) {
						t.Errorf("mismatch on channel %d", id)
					}
				case <-ctx.Done():
					cancel()
				}
			}
			_ = ch.Close()
		}(i)
	}

	wg.Wait()
}

func TestMultiplex_ActualChaos(t *testing.T) {
	upgrader := multiplex.Upgrader{
		Upgrader: websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }},
	}

	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		c.SetChannelCreatedHandler(func(ch *multiplex.Channel) error {
			for {
				msg, err := ch.ReadMessage()
				if err != nil {
					return nil
				}
				_ = ch.WriteMessage(msg)
			}
		})
		time.Sleep(1 * time.Second)
	}))
	defer s.Close()

	u := "ws" + strings.TrimPrefix(s.URL, "http")

	dialer := ChaosDialer{DropRate: 0.10}
	clientConn, _, err := dialer.Dial(context.Background(), u, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientConn.Close()

	ch, err := clientConn.CreateChannel(1)
	if err != nil {
		t.Fatal(err)
	}

	successCount := 0
	for i := 0; i < 20; i++ {
		data := []byte("chaos-msg")
		_ = ch.WriteMessage(data)

		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		readErr := make(chan error, 1)
		go func() {
			_, err := ch.ReadMessage()
			readErr <- err
		}()

		select {
		case err := <-readErr:
			if err == nil {
				successCount++
			}
		case <-ctx.Done():
		}
		cancel()
	}

	t.Logf("Chaos test finished. Successes: %d/20", successCount)
}
