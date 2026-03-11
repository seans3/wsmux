// Copyright 2026 Sean Sullivan.
// SPDX-License-Identifier: MIT

//go:build e2e

// Package e2e contains end-to-end tests that exercise the ws-file-server and
// ws-file-client binaries over a real network connection.
//
// Run with:
//
//	make test-e2e
package e2e

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// Paths to the built binaries, set in TestMain.
var (
	wsFileServer string
	wsFileClient string
)

// TestMain builds the binaries once before any test runs, then tears down the
// temp directory afterwards.
func TestMain(m *testing.M) {
	tmpDir, err := os.MkdirTemp("", "ws-e2e-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create temp dir: %v\n", err)
		os.Exit(1)
	}
	defer os.RemoveAll(tmpDir)

	wsFileServer = filepath.Join(tmpDir, "ws-file-server")
	wsFileClient = filepath.Join(tmpDir, "ws-file-client")

	for pkg, out := range map[string]string{
		"github.com/seans3/wsmux/cmd/ws-file-server": wsFileServer,
		"github.com/seans3/wsmux/cmd/ws-file-client": wsFileClient,
	} {
		if out, err := exec.Command("go", "build", "-o", out, pkg).CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "build %s: %v\n%s\n", pkg, err, out)
			os.Exit(1)
		}
	}

	os.Exit(m.Run())
}

// freePort finds an available TCP port on localhost.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatalf("freePort: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port
}

// runTransfer starts ws-file-server, pipes src into ws-file-client, waits for
// both to finish, and returns the bytes the server received.
//
// serverArgs and clientArgs are additional flags passed to each binary
// (e.g. "-flow-control", "-window", "4096").
func runTransfer(t *testing.T, src []byte, serverArgs, clientArgs []string) []byte {
	t.Helper()

	port := freePort(t)
	addr := fmt.Sprintf("localhost:%d", port)

	// Capture server stdout (the received file data) and stderr (log lines).
	var serverOut bytes.Buffer
	var serverLog bytes.Buffer

	serverCmd := exec.Command(wsFileServer, append([]string{"-addr", addr}, serverArgs...)...)
	serverCmd.Stdout = &serverOut
	serverCmd.Stderr = &serverLog

	if err := serverCmd.Start(); err != nil {
		t.Fatalf("start server: %v", err)
	}

	// Wait until the server is accepting connections.
	if err := waitForPort(addr, 2*time.Second); err != nil {
		serverCmd.Process.Kill()
		t.Fatalf("server never became ready: %v\nserver log:\n%s", err, serverLog.String())
	}

	// Run client, feeding src on stdin.
	var clientLog bytes.Buffer
	clientCmd := exec.Command(wsFileClient, append([]string{"-addr", addr}, clientArgs...)...)
	clientCmd.Stdin = bytes.NewReader(src)
	clientCmd.Stderr = &clientLog

	if err := clientCmd.Run(); err != nil {
		serverCmd.Process.Kill()
		t.Fatalf("client failed: %v\nclient log:\n%s", err, clientLog.String())
	}

	// Server should exit on its own once the transfer is done.
	done := make(chan error, 1)
	go func() { done <- serverCmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("server exited with error: %v\nserver log:\n%s", err, serverLog.String())
		}
	case <-time.After(5 * time.Second):
		serverCmd.Process.Kill()
		t.Fatalf("server did not exit after client finished\nserver log:\n%s", serverLog.String())
	}

	t.Logf("server: %s", serverLog.String())
	t.Logf("client: %s", clientLog.String())

	return serverOut.Bytes()
}

// waitForPort polls until the TCP address is accepting connections.
func waitForPort(addr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 50*time.Millisecond)
		if err == nil {
			conn.Close()
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return fmt.Errorf("port %s not ready after %v", addr, timeout)
}

// TestE2E_FileTransfer_NoFlowControl sends 10MB without flow control and
// verifies the received bytes are identical to what was sent.
func TestE2E_FileTransfer_NoFlowControl(t *testing.T) {
	const size = 10 * 1024 * 1024 // 10MB

	src := make([]byte, size)
	if _, err := rand.Read(src); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}

	got := runTransfer(t, src, nil, nil)

	if !bytes.Equal(got, src) {
		t.Errorf("received %d bytes, want %d; data mismatch", len(got), len(src))
	}
}

// TestE2E_FileTransfer_FlowControlDefaultWindow sends 10MB with flow control
// enabled and the default 64KB window — the normal production configuration.
func TestE2E_FileTransfer_FlowControlDefaultWindow(t *testing.T) {
	const size = 10 * 1024 * 1024 // 10MB

	src := make([]byte, size)
	if _, err := rand.Read(src); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}

	fcArgs := []string{"-flow-control"}
	got := runTransfer(t, src, fcArgs, fcArgs)

	if !bytes.Equal(got, src) {
		t.Errorf("received %d bytes, want %d; data mismatch", len(got), len(src))
	}
}

// TestE2E_FileTransfer_FlowControl4KWindow sends 10MB with flow control enabled
// and a 4KB window. With a 4KB window the sender must go through ~2560 window
// replenishment cycles to deliver 10MB, strongly exercising the backpressure path.
func TestE2E_FileTransfer_FlowControl4KWindow(t *testing.T) {
	const size = 10 * 1024 * 1024 // 10MB
	const window = "4096"         // 4KB — far smaller than io.Copy's 32KB buffer

	src := make([]byte, size)
	if _, err := rand.Read(src); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}

	fcArgs := []string{"-flow-control", "-window", window}
	got := runTransfer(t, src, fcArgs, fcArgs)

	if !bytes.Equal(got, src) {
		t.Errorf("received %d bytes, want %d; data mismatch", len(got), len(src))
	}
}
