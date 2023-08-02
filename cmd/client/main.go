// Copyright 2023 Sean Sullivan.
// SPDX-License-Identifier: MIT
package main

import (
	"bufio"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
)

var addr = flag.String("addr", "localhost:8080", "address/port of websocket server")

const (
	writeWait        = 10 * time.Second
	closeGracePeriod = 10 * time.Second
	handshakeTimeout = 45 * time.Second
)

var dialer = &websocket.Dialer{
	Proxy:            http.ProxyFromEnvironment,
	Subprotocols:     []string{},
	HandshakeTimeout: handshakeTimeout,
	ReadBufferSize:   32 * 1024,
	WriteBufferSize:  32 * 1024,
}

func main() {
	flag.Parse()
	fmt.Printf("starting websocket client...%s\n", *addr)
	// Dial websocket connection.
	u := url.URL{Scheme: "ws", Host: *addr, Path: "/echo"}
	conn, _, err := dialer.Dial(u.String(), nil)
	if err != nil {
		fmt.Printf("dial: %v", err)
		os.Exit(1)
	}
	defer conn.Close()
	// Set up handler for interrupt signal
	signalCh := make(chan os.Signal, 1)
	signal.Notify(signalCh, syscall.SIGINT, syscall.SIGTERM)
	// Create and start reading loop
	go func() {
		defer conn.Close()
		// conn.SetReadDeadline(time.Now().Add(pongWait))
		for {
			_, message, err := conn.ReadMessage()
			if err != nil {
				break
			}
			message = append(message, '\n')
			fmt.Printf("%s", message)
		}
	}()
	go func() {
		// Create and run loop to read stdin and write to the websocket connection.
		s := bufio.NewScanner(bufio.NewReader(os.Stdin))
		for s.Scan() {
			conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := conn.WriteMessage(websocket.TextMessage, s.Bytes()); err != nil {
				conn.Close()
				break
			}
		}
		if s.Err() != nil {
			fmt.Printf("scan: %s", s.Err())
		}
	}()

	select {
	case <-signalCh:
		fmt.Println("caught interrupt signal--closing")
		break
	}

	fmt.Println("sending close to server...")
	conn.SetWriteDeadline(time.Now().Add(writeWait))
	conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
	time.Sleep(closeGracePeriod)
	conn.Close()

	fmt.Println("websocket client...finished")
}
