// Copyright 2023 Sean Sullivan.
// SPDX-License-Identifier: MIT

// This file verifies that logical channels correctly implement the standard 
// io.Reader and io.Writer interfaces, ensuring compatibility with Go's 
// standard library tools like io.Copy and bufio.
package multiplex

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
)

func TestMultiplex_IOInterfaces(t *testing.T) {
	upgrader := Upgrader{
		websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }},
		0, 0,
	}

	serverConnCh := make(chan *Conn, 1)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		serverConnCh <- c
	}))
	defer s.Close()

	u := "ws" + strings.TrimPrefix(s.URL, "http")
	dialer := Dialer{Dialer: websocket.Dialer{}}
	clientConn, _, err := dialer.Dial(context.Background(), u, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientConn.Close()
	serverConn := <-serverConnCh
	defer serverConn.Close()

	clientCh, _ := clientConn.CreateChannel(1)
	serverCh := <-waitForChannel(serverConn)

	// Test io.Writer -> io.Reader
	testMsg := "this is a test message for io interfaces"
	go func() {
		_, _ = io.WriteString(clientCh, testMsg)
		_ = clientCh.CloseWrite()
	}()

	// Read in small chunks to test remainingBuf
	buf := make([]byte, 10)
	var result bytes.Buffer
	for {
		n, err := serverCh.Read(buf)
		if n > 0 {
			result.Write(buf[:n])
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Read error: %v", err)
		}
	}

	if result.String() != testMsg {
		t.Errorf("Expected %q, got %q", testMsg, result.String())
	}
}
