// Copyright 2026 Sean Sullivan.
// SPDX-License-Identifier: MIT

// This file contains core functional tests for the multiplexing library,
// including handshake negotiation, channel creation, and basic data transfer.
package multiplex

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/seans3/wsmux/pkg/multiplex/internal/protocol"
)

func TestMultiplex_Handshake(t *testing.T) {
	t.Run("Successful Negotiation", func(t *testing.T) {
		var upgrader = Upgrader{
			Upgrader: websocket.Upgrader{
				CheckOrigin: func(r *http.Request) bool { return true },
			},
		}

		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				return
			}
			defer c.Close()
		}))
		defer s.Close()

		u := "ws" + strings.TrimPrefix(s.URL, "http")
		dialer := Dialer{Dialer: websocket.Dialer{}}
		clientConn, _, err := dialer.Dial(context.Background(), u, nil)
		if err != nil {
			t.Fatalf("Dial failed: %v", err)
		}
		defer clientConn.Close()

		if clientConn.Subprotocol() != protocol.ProtocolVersion {
			t.Errorf("Expected subprotocol %s, got %s", protocol.ProtocolVersion, clientConn.Subprotocol())
		}
	})

	t.Run("Upgrade Failure - No Websocket", func(t *testing.T) {
		var upgrader = Upgrader{Upgrader: websocket.Upgrader{}}
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, err := upgrader.Upgrade(w, r, nil)
			if err == nil {
				t.Error("Expected error on non-websocket upgrade, got nil")
			}
		}))
		defer s.Close()

		resp, err := http.Get(s.URL)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("Expected status 400, got %d", resp.StatusCode)
		}
	})

	t.Run("Dial Failure - Connection Refused", func(t *testing.T) {
		dialer := Dialer{Dialer: websocket.Dialer{}}
		_, _, err := dialer.Dial(context.Background(), "ws://localhost:1", nil)
		if err == nil {
			t.Error("Expected error on refused connection, got nil")
		}
	})
}

func TestMultiplex_Basic(t *testing.T) {
	// Setup a test server
	var upgrader = Upgrader{
		Upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
	}

	serverConnCh := make(chan *Conn, 1)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("Upgrade failed: %v", err)
			return
		}
		serverConnCh <- c
	}))
	defer s.Close()

	// Client Dials
	u := "ws" + strings.TrimPrefix(s.URL, "http")
	dialer := Dialer{Dialer: websocket.Dialer{}}
	clientConn, _, err := dialer.Dial(context.Background(), u, nil)
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}
	defer clientConn.Close()

	serverConn := <-serverConnCh
	defer serverConn.Close()

	// Test Channel Creation (Client -> Server)
	const channelID = 100
	clientCh, err := clientConn.CreateChannel(channelID)
	if err != nil {
		t.Fatalf("CreateChannel failed: %v", err)
	}

	// Wait for server to receive the channel
	var serverCh *Channel
	select {
	case serverCh = <-waitForChannel(serverConn):
	case <-time.After(time.Second):
		t.Fatal("Timeout waiting for server channel creation")
	}

	if serverCh.GetChannelID() != channelID {
		t.Errorf("Expected channel ID %d, got %d", channelID, serverCh.GetChannelID())
	}

	// Test Data Transfer (Client -> Server)
	testData := []byte("hello from client")
	if err := clientCh.WriteMessage(testData); err != nil {
		t.Fatalf("WriteMessage failed: %v", err)
	}

	select {
	case received := <-readMessageAsync(serverCh):
		if !bytes.Equal(received, testData) {
			t.Errorf("Expected %s, got %s", testData, received)
		}
	case <-time.After(time.Second):
		t.Fatal("Timeout waiting for data at server")
	}

	// Test Data Transfer (Server -> Client)
	testDataServer := []byte("hello from server")
	if err := serverCh.WriteMessage(testDataServer); err != nil {
		t.Fatalf("WriteMessage failed: %v", err)
	}

	select {
	case received := <-readMessageAsync(clientCh):
		if !bytes.Equal(received, testDataServer) {
			t.Errorf("Expected %s, got %s", testDataServer, received)
		}
	case <-time.After(time.Second):
		t.Fatal("Timeout waiting for data at client")
	}

	// Test Channel Closure
	if err := clientCh.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	// Server should see the closure
	select {
	case _, ok := <-readMessageAsync(serverCh):
		if ok {
			t.Error("Expected channel to be closed at server")
		}
	case <-time.After(time.Second * 2):
		t.Fatal("Timeout waiting for closure at server")
	}
}

func waitForChannel(c *Conn) chan *Channel {
	ch := make(chan *Channel, 1)
	c.SetChannelCreatedHandler(func(newCh *Channel) error {
		ch <- newCh
		return nil
	})
	return ch
}

func TestMultiplex_MaxChannels(t *testing.T) {
	t.Run("outbound limit enforced", func(t *testing.T) {
		upgrader := Upgrader{
			Upgrader: websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }},
		}
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				return
			}
			<-c.Done()
		}))
		defer s.Close()

		u := "ws" + strings.TrimPrefix(s.URL, "http")
		dialer := Dialer{Dialer: websocket.Dialer{}, MaxChannels: 2}
		conn, _, err := dialer.Dial(context.Background(), u, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()

		if _, err := conn.CreateChannel(1); err != nil {
			t.Fatalf("first channel: %v", err)
		}
		if _, err := conn.CreateChannel(2); err != nil {
			t.Fatalf("second channel: %v", err)
		}
		if _, err := conn.CreateChannel(3); err != ErrTooManyChannels {
			t.Fatalf("expected ErrTooManyChannels, got %v", err)
		}
	})

	t.Run("inbound limit closes connection", func(t *testing.T) {
		upgrader := Upgrader{
			Upgrader:    websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }},
			MaxChannels: 2,
		}
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				return
			}
			c.SetChannelCreatedHandler(func(ch *Channel) error { return nil })
			<-c.Done()
		}))
		defer s.Close()

		u := "ws" + strings.TrimPrefix(s.URL, "http")
		dialer := Dialer{Dialer: websocket.Dialer{}}
		conn, _, err := dialer.Dial(context.Background(), u, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()

		_, _ = conn.CreateChannel(1)
		_, _ = conn.CreateChannel(2)
		_, _ = conn.CreateChannel(3) // server should close connection on this one

		select {
		case <-conn.Done():
			// expected: server closed the connection
		case <-time.After(2 * time.Second):
			t.Fatal("connection was not closed after exceeding server MaxChannels")
		}
	})
}

func readMessageAsync(ch *Channel) chan []byte {
	out := make(chan []byte, 1)
	go func() {
		msg, err := ch.ReadMessage()
		if err == nil {
			out <- msg
		} else {
			close(out)
		}
	}()
	return out
}
