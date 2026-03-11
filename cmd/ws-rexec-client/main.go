// Copyright 2026 Sean Sullivan.
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"flag"
	"io"
	"log"
	"net/url"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/gorilla/websocket"
	"github.com/seans3/websockets/pkg/multiplex"
)

var (
	addr          = flag.String("addr", "localhost:8080", "http service address")
	flowControl   = flag.Bool("flow-control", false, "enable per-channel flow control")
	initialWindow = flag.Uint("window", 0, "flow control initial window in bytes (0 = default 64KB)")
)

const (
	ChanStdin  = 0
	ChanStdout = 1
	ChanStderr = 2
)

func main() {
	flag.Parse()
	log.SetFlags(0)

	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt, syscall.SIGTERM)

	u := url.URL{Scheme: "ws", Host: *addr, Path: "/rexec"}
	log.Printf("connecting to %s", u.String())

	dialer := multiplex.Dialer{
		Dialer:            websocket.Dialer{},
		EnableFlowControl: *flowControl,
		InitialWindow:     uint32(*initialWindow),
	}
	conn, _, err := dialer.Dial(context.Background(), u.String(), nil)
	if err != nil {
		log.Fatal("dial:", err)
	}
	defer conn.Close()

	stdin, err := conn.CreateChannel(ChanStdin)
	if err != nil {
		log.Fatal("create stdin chan:", err)
	}
	stdout, err := conn.CreateChannel(ChanStdout)
	if err != nil {
		log.Fatal("create stdout chan:", err)
	}
	stderr, err := conn.CreateChannel(ChanStderr)
	if err != nil {
		log.Fatal("create stderr chan:", err)
	}

	var wg sync.WaitGroup
	wg.Add(3)

	// STDIN: Local Stdin -> Server
	go func() {
		defer wg.Done()
		defer func() { _ = stdin.CloseWrite() }()
		_, _ = io.Copy(stdin, os.Stdin)
	}()

	// STDOUT: Server -> Local Stdout
	go func() {
		defer wg.Done()
		_, _ = io.Copy(os.Stdout, stdout)
	}()

	// STDERR: Server -> Local Stderr
	go func() {
		defer wg.Done()
		_, _ = io.Copy(os.Stderr, stderr)
	}()

	// Wait for connection to close or interrupt
	select {
	case <-conn.Done():
		log.Println("connection closed by server")
	case <-interrupt:
		log.Println("interrupted")
	}

	// We don't necessarily wait for wg here because os.Stdin copy
	// might be blocking on user input, and we want to exit fast on interrupt.
}
