// Copyright 2023 Sean Sullivan.
// SPDX-License-Identifier: MIT

package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os/exec"
	"sync"

	"github.com/gorilla/websocket"
	"github.com/seans3/websockets/pkg/multiplex"
)

var (
	addr = flag.String("addr", "localhost:8080", "http service address")
)

const (
	ChanStdin  = 0
	ChanStdout = 1
	ChanStderr = 2
)

func main() {
	flag.Parse()
	log.SetFlags(0)

	http.HandleFunc("/rexec", handleRexec)

	log.Printf("ws-rexec-server listening on %s", *addr)
	log.Fatal(http.ListenAndServe(*addr, nil))
}

func handleRexec(w http.ResponseWriter, r *http.Request) {
	upgrader := multiplex.Upgrader{
		Upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Print("upgrade:", err)
		return
	}
	defer conn.Close()

	log.Printf("new rexec connection from %s", r.RemoteAddr)

	var (
		mu     sync.Mutex
		stdin  *multiplex.Channel
		stdout *multiplex.Channel
		stderr *multiplex.Channel
	)

	ready := make(chan struct{})
	once := sync.Once{}

	conn.SetChannelCreatedHandler(func(ch *multiplex.Channel) error {
		mu.Lock()
		defer mu.Unlock()

		id := ch.GetChannelID()
		switch id {
		case ChanStdin:
			stdin = ch
		case ChanStdout:
			stdout = ch
		case ChanStderr:
			stderr = ch
		default:
			return fmt.Errorf("unexpected channel ID: %d", id)
		}

		if stdin != nil && stdout != nil && stderr != nil {
			once.Do(func() { close(ready) })
		}
		return nil
	})

	select {
	case <-ready:
		log.Print("all channels ready, starting bash")
	case <-conn.Done():
		log.Print("connection closed before channels ready")
		return
	}

	// For this example, we'll just run bash.
	cmd := exec.Command("bash")

	cmdStdin, _ := cmd.StdinPipe()
	cmdStdout, _ := cmd.StdoutPipe()
	cmdStderr, _ := cmd.StderrPipe()

	if err := cmd.Start(); err != nil {
		log.Printf("failed to start cmd: %v", err)
		return
	}

	var wg sync.WaitGroup
	wg.Add(3)

	// Pipe multiplexed channels to/from process pipes

	// STDIN: Client -> Server -> Bash
	go func() {
		defer wg.Done()
		defer cmdStdin.Close()
		_, _ = io.Copy(cmdStdin, stdin)
		log.Print("stdin closed")
	}()

	// STDOUT: Bash -> Server -> Client
	go func() {
		defer wg.Done()
		defer stdout.CloseWrite()
		_, _ = io.Copy(stdout, cmdStdout)
		log.Print("stdout finished")
	}()

	// STDERR: Bash -> Server -> Client
	go func() {
		defer wg.Done()
		defer stderr.CloseWrite()
		_, _ = io.Copy(stderr, cmdStderr)
		log.Print("stderr finished")
	}()

	// Wait for process to exit
	err = cmd.Wait()
	log.Printf("process exited with err: %v", err)

	// Ensure all piping goroutines finish
	wg.Wait()
	log.Print("rexec handler finished")
}
