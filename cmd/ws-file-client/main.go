// Copyright 2026 Sean Sullivan.
// SPDX-License-Identifier: MIT

// ws-file-client connects to ws-file-server, opens a single channel, and
// streams stdin into it. Used for manual flow control testing.
package main

import (
	"context"
	"flag"
	"io"
	"log"
	"net/url"
	"os"

	"github.com/gorilla/websocket"
	"github.com/seans3/websockets/pkg/multiplex"
)

var (
	addr          = flag.String("addr", "localhost:9090", "server address")
	flowControl   = flag.Bool("flow-control", false, "enable per-channel flow control")
	initialWindow = flag.Uint("window", 0, "flow control initial window in bytes (0 = default 64KB)")
)

const transferChannel = 1

func main() {
	flag.Parse()
	log.SetFlags(0)

	u := url.URL{Scheme: "ws", Host: *addr, Path: "/transfer"}

	dialer := multiplex.Dialer{
		Dialer:            websocket.Dialer{},
		EnableFlowControl: *flowControl,
		InitialWindow:     uint32(*initialWindow),
	}
	conn, _, err := dialer.Dial(context.Background(), u.String(), nil)
	if err != nil {
		log.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	ch, err := conn.CreateChannel(transferChannel)
	if err != nil {
		log.Fatalf("create channel: %v", err)
	}

	n, err := io.Copy(ch, os.Stdin)
	if err != nil {
		log.Fatalf("copy: %v", err)
	}
	if err := ch.CloseWrite(); err != nil {
		log.Fatalf("close write: %v", err)
	}

	log.Printf("sent %d bytes", n)

	// Wait for the connection to be closed by the server after it finishes receiving.
	<-conn.Done()
}
