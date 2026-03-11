// Copyright 2026 Sean Sullivan.
// SPDX-License-Identifier: MIT

//go:build long

// Shared test helpers used by the long-running integration tests.
package integration

import (
	"github.com/seans3/websockets/pkg/multiplex"
)

// readMessageAsync calls ReadMessage on ch in a goroutine and returns a channel
// that receives the message, or is closed on error.
func readMessageAsync(ch *multiplex.Channel) chan []byte {
	out := make(chan []byte, 1)
	go func() {
		msg, err := ch.ReadMessage()
		if err == nil {
			out <- msg
		} else {
			close(out)
		}
	}()
	return out
}

// encodeFrame encodes a multiplexer frame as raw bytes using the wire format:
//
//	[varint channelID][1-byte flag][payload]
//
// This mirrors the internal protocol package encoding without importing it,
// allowing the integration tests to construct arbitrary (potentially malformed)
// frames without depending on internal packages.
func encodeFrame(channelID uint64, flag byte, payload []byte) []byte {
	var buf []byte
	// Varint-encode the channel ID (base-128, little-endian, MSB = continuation).
	v := channelID
	for v >= 0x80 {
		buf = append(buf, byte(v)|0x80)
		v >>= 7
	}
	buf = append(buf, byte(v))
	buf = append(buf, flag)
	buf = append(buf, payload...)
	return buf
}
