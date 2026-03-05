// Copyright 2023 Sean Sullivan.
// SPDX-License-Identifier: MIT

package main

import (
	"flag"
	"fmt"
	"net/http"

	"github.com/gorilla/websocket"
	"github.com/seans3/websockets/pkg/multiplex"
)

var addr = flag.String("addr", "localhost:8080", "address/port of websocket server")

func main() {
	flag.Parse()
	fmt.Printf("starting multiplexed websocket server...%s\n", *addr)

	http.HandleFunc("/echo", echoHandler)
	if err := http.ListenAndServe(*addr, nil); err != nil {
		fmt.Printf("server error: %v\n", err)
	}
}

var upgrader = websocket.Upgrader{
	ReadBufferSize:  32 * 1024,
	WriteBufferSize: 32 * 1024,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

func echoHandler(w http.ResponseWriter, r *http.Request) {
	mUpgrader := multiplex.Upgrader{Upgrader: upgrader}
	conn, err := mUpgrader.Upgrade(w, r, nil)
	if err != nil {
		fmt.Printf("Error upgrading websocket request: %v\n", err)
		return
	}
	defer conn.Close()

	fmt.Printf("Client connected with subprotocol: %s\n", conn.Subprotocol())

	conn.SetChannelCreatedHandler(func(ch *multiplex.Channel) error {
		fmt.Printf("New logical channel created: %d\n", ch.GetChannelID())
		go func() {
			defer ch.Close()
			for {
				msg, err := ch.ReadMessage()
				if err != nil {
					fmt.Printf("Channel %d closed: %v\n", ch.GetChannelID(), err)
					return
				}
				fmt.Printf("Channel %d: received %d bytes\n", ch.GetChannelID(), len(msg))
				if err := ch.WriteMessage(msg); err != nil {
					return
				}
			}
		}()
		return nil
	})

	// Wait for connection to terminate
	<-conn.Done()
	fmt.Println("websocket server handler finished")
}
