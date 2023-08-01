// Copyright 2023 Sean Sullivan.
// SPDX-License-Identifier: MIT
package main

import (
	"flag"
	"fmt"
	"net/http"

	"github.com/gorilla/websocket"
)

var addr = flag.String("addr", "localhost:8080", "address/port of websocket server")

func main() {
	flag.Parse()
	fmt.Printf("starting websocket server...%s\n", *addr)
	fmt.Println("")

	http.HandleFunc("/echo", echoHandler)
	http.ListenAndServe(*addr, nil)

	fmt.Println("websocket server...finished")
}

var upgrader = websocket.Upgrader{}

// echoHandler upgrades passed request to a websocket connection, and reads
// this connection. Implements http.Handler interface.
func echoHandler(w http.ResponseWriter, r *http.Request) {
	c, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		fmt.Printf("Error upgrading websocket request: %v", err)
		return
	}
	defer c.Close()
	for {
		messageType, message, err := c.ReadMessage()
		if err != nil {
			websocketErr, ok := err.(*websocket.CloseError)
			if ok && websocketErr.Code == websocket.CloseNormalClosure {
				err = nil // readers will get io.EOF as it's a normal closure
			} else {
				fmt.Errorf("next reader: %w", err)
			}
			break
		}
		fmt.Printf("Received: %s\n", message)
		err = c.WriteMessage(messageType, message)
		if err != nil {
			fmt.Printf("Error writing message to websocket: %v", err)
			break
		}
	}
}
