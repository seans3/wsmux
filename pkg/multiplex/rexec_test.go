// Copyright 2023 Sean Sullivan.
// SPDX-License-Identifier: MIT

//go:build long

package multiplex

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestMultiplex_Rexec(t *testing.T) {
	// 1. Setup Server (Simulating ws-rexec-server)
	upgrader := Upgrader{
		Upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
	}

	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		var (
			mu     sync.Mutex
			stdin  *Channel
			stdout *Channel
			stderr *Channel
		)
		ready := make(chan struct{})

		conn.SetChannelCreatedHandler(func(ch *Channel) error {
			mu.Lock()
			defer mu.Unlock()
			switch ch.GetChannelID() {
			case 0:
				stdin = ch
			case 1:
				stdout = ch
			case 2:
				stderr = ch
			}
			if stdin != nil && stdout != nil && stderr != nil {
				close(ready)
			}
			return nil
		})

		select {
		case <-ready:
		case <-time.After(5 * time.Second):
			return
		}

		// Use 'cat' to test bidirectional piping
		cmd := exec.Command("cat")
		cmdStdin, _ := cmd.StdinPipe()
		cmdStdout, _ := cmd.StdoutPipe()
		cmdStderr, _ := cmd.StderrPipe()

		if err := cmd.Start(); err != nil {
			return
		}

		go func() {
			_, _ = io.Copy(cmdStdin, stdin)
			_ = cmdStdin.Close()
		}()
		go func() {
			_, _ = io.Copy(stdout, cmdStdout)
			_ = stdout.CloseWrite()
		}()
		go func() {
			_, _ = io.Copy(stderr, cmdStderr)
			_ = stderr.CloseWrite()
		}()

		_ = cmd.Wait()
	}))
	defer s.Close()

	// 2. Setup Client (Simulating ws-rexec-client)
	u := "ws" + strings.TrimPrefix(s.URL, "http")
	dialer := Dialer{Dialer: websocket.Dialer{}}
	clientConn, _, err := dialer.Dial(context.Background(), u, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientConn.Close()

	stdin, _ := clientConn.CreateChannel(0)
	stdout, _ := clientConn.CreateChannel(1)
	_, _ = clientConn.CreateChannel(2)

	testMsg := "hello rexec"

	// Write to stdin
	go func() {
		_, _ = stdin.Write([]byte(testMsg))
		_ = stdin.CloseWrite()
	}()

	// Read from stdout
	resultCh := make(chan string)
	go func() {
		data, _ := io.ReadAll(stdout)
		resultCh <- string(data)
	}()

	select {
	case result := <-resultCh:
		if result != testMsg {
			t.Errorf("Expected %q, got %q", testMsg, result)
		}
	case <-time.After(5 * time.Second):
		t.Error("Timed out waiting for rexec response")
	}
}
