// Copyright 2026 Sean Sullivan.
// SPDX-License-Identifier: MIT

package protocol

import (
	"encoding/binary"
	"errors"
)

// ProtocolVersion is the Sec-WebSocket-Protocol value used for negotiation.
const ProtocolVersion = "multiplex.v1.0"

// Flag types for the multiplexing protocol.
const (
	FlagData         byte = 0x01
	FlagCreate       byte = 0x02
	FlagClose        byte = 0x03
	FlagEOF          byte = 0x04
	FlagWindowUpdate byte = 0x05
)

var (
	ErrFrameTooShort = errors.New("frame too short")
	ErrInvalidVarint = errors.New("invalid varint")
)

// Frame represents a single multiplexed message frame.
type Frame struct {
	ChannelID uint64
	Flag      byte
	Payload   []byte
}

// Encode serializes a Frame into a byte slice.
func (f *Frame) Encode() []byte {
	// Max varint size for uint64 is 10 bytes + 1 byte for flag
	buf := make([]byte, 11+len(f.Payload))
	n := binary.PutUvarint(buf, f.ChannelID)
	buf[n] = f.Flag
	copy(buf[n+1:], f.Payload)
	return buf[:n+1+len(f.Payload)]
}

// Decode deserializes a byte slice into a Frame.
func Decode(data []byte) (*Frame, error) {
	id, n := binary.Uvarint(data)
	if n == 0 {
		return nil, ErrFrameTooShort
	}
	if n < 0 {
		return nil, ErrInvalidVarint
	}
	if len(data) < n+1 {
		return nil, ErrFrameTooShort
	}
	f := &Frame{
		ChannelID: id,
		Flag:      data[n],
		Payload:   data[n+1:],
	}
	return f, nil
}

// EncodeWindowUpdate creates a Frame for a window update.
func EncodeWindowUpdate(channelID uint64, increment uint32) *Frame {
	payload := make([]byte, 4)
	binary.BigEndian.PutUint32(payload, increment)
	return &Frame{
		ChannelID: channelID,
		Flag:      FlagWindowUpdate,
		Payload:   payload,
	}
}

// DecodeWindowUpdate extracts the window increment from a frame payload.
func DecodeWindowUpdate(payload []byte) (uint32, error) {
	if len(payload) < 4 {
		return 0, ErrFrameTooShort
	}
	return binary.BigEndian.Uint32(payload[:4]), nil
}
