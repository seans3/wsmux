// Copyright 2026 Sean Sullivan.
// SPDX-License-Identifier: MIT

package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"github.com/seans3/websockets/pkg/multiplex"
)

var addr = flag.String("addr", "localhost:8080", "address/port of websocket server")

func main() {
	flag.Parse()
	fmt.Printf("starting multiplexed websocket client...%s\n", *addr)

	u := url.URL{Scheme: "ws", Host: *addr, Path: "/echo"}
	dialer := multiplex.Dialer{Dialer: websocket.Dialer{
		HandshakeTimeout: 45 * time.Second,
	}}

	conn, _, err := dialer.Dial(context.Background(), u.String(), nil)
	if err != nil {
		fmt.Printf("dial error: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()

	fmt.Printf("Connected with subprotocol: %s\n", conn.Subprotocol())

	// Create a single logical channel for our echo test
	ch, err := conn.CreateChannel(1)
	if err != nil {
		fmt.Printf("failed to create channel: %v\n", err)
		os.Exit(1)
	}

	// Handle interrupt signals
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// Read from channel and print to stdout
	go func() {
		for {
			msg, err := ch.ReadMessage()
			if err != nil {
				fmt.Printf("\nchannel read error: %v\n", err)
				return
			}
			fmt.Printf("Echo: %s\n", string(msg))
		}
	}()

	// Read from stdin and write to channel
	go func() {
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			text := scanner.Bytes()
			if err := ch.WriteMessage(text); err != nil {
				fmt.Printf("channel write error: %v\n", err)
				return
			}
		}
	}()

	select {
	case <-sigCh:
		fmt.Println("\nclosing connection...")
	case <-conn.Done():
		fmt.Println("\nserver closed connection")
	}
}
