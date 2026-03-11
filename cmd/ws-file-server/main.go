// Copyright 2026 Sean Sullivan.
// SPDX-License-Identifier: MIT

// ws-file-server listens for a single multiplexed connection, receives all
// data from channel 1, and writes it to stdout. Used for manual flow control
// testing in conjunction with ws-file-client.
package main

import (
	"flag"
	"io"
	"log"
	"net/http"
	"os"

	"github.com/gorilla/websocket"
	"github.com/seans3/websockets/pkg/multiplex"
)

var (
	addr          = flag.String("addr", "localhost:9090", "listen address")
	flowControl   = flag.Bool("flow-control", false, "enable per-channel flow control")
	initialWindow = flag.Uint("window", 0, "flow control initial window in bytes (0 = default 64KB)")
)

const transferChannel = 1

func main() {
	flag.Parse()
	log.SetFlags(0)

	done := make(chan struct{})

	http.HandleFunc("/transfer", func(w http.ResponseWriter, r *http.Request) {
		upgrader := multiplex.Upgrader{
			Upgrader:          websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }},
			EnableFlowControl: *flowControl,
			InitialWindow:     uint32(*initialWindow),
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Printf("upgrade: %v", err)
			return
		}
		defer conn.Close()

		chReady := make(chan *multiplex.Channel, 1)
		conn.SetChannelCreatedHandler(func(ch *multiplex.Channel) error {
			if ch.GetChannelID() == transferChannel {
				chReady <- ch
			}
			return nil
		})

		var ch *multiplex.Channel
		select {
		case ch = <-chReady:
		case <-conn.Done():
			log.Print("connection closed before channel arrived")
			return
		}

		n, err := io.Copy(os.Stdout, ch)
		if err != nil {
			log.Printf("copy error: %v", err)
		}
		log.Printf("received %d bytes", n)
		conn.Close()
		close(done)
	})

	log.Printf("ws-file-server listening on %s (flow-control=%v window=%d)", *addr, *flowControl, *initialWindow)
	go func() {
		if err := http.ListenAndServe(*addr, nil); err != nil {
			log.Fatal(err)
		}
	}()
	<-done
}
