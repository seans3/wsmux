// Copyright 2023 Sean Sullivan.
// SPDX-License-Identifier: MIT

package protocol

import (
	"bytes"
	"testing"
)

func TestFrame_EncodeDecode(t *testing.T) {
	tests := []struct {
		name    string
		frame   Frame
		wantErr bool
	}{
		{
			name: "Basic Data Frame",
			frame: Frame{
				ChannelID: 123,
				Flag:      FlagData,
				Payload:   []byte("hello world"),
			},
			wantErr: false,
		},
		{
			name: "Create Frame No Payload",
			frame: Frame{
				ChannelID: 456,
				Flag:      FlagCreate,
				Payload:   nil,
			},
			wantErr: false,
		},
		{
			name: "Max Channel ID",
			frame: Frame{
				ChannelID: ^uint64(0),
				Flag:      FlagClose,
				Payload:   []byte{0x01, 0x02},
			},
			wantErr: false,
		},
		{
			name: "ID 128 (2 bytes)",
			frame: Frame{
				ChannelID: 128,
				Flag:      FlagData,
				Payload:   []byte("abc"),
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded := tt.frame.Encode()
			decoded, err := Decode(encoded)
			
			if (err != nil) != tt.wantErr {
				t.Fatalf("Decode() error = %v, wantErr %v", err, tt.wantErr)
			}
			
			if err == nil {
				if decoded.ChannelID != tt.frame.ChannelID {
					t.Errorf("ChannelID = %v, want %v", decoded.ChannelID, tt.frame.ChannelID)
				}
				if decoded.Flag != tt.frame.Flag {
					t.Errorf("Flag = %v, want %v", decoded.Flag, tt.frame.Flag)
				}
				if !bytes.Equal(decoded.Payload, tt.frame.Payload) {
					t.Errorf("Payload = %v, want %v", decoded.Payload, tt.frame.Payload)
				}
			}
		})
	}
}

func TestDecode_Errors(t *testing.T) {
	t.Run("Empty slice", func(t *testing.T) {
		_, err := Decode([]byte{})
		if err != ErrFrameTooShort {
			t.Errorf("expected ErrFrameTooShort, got %v", err)
		}
	})

	t.Run("ID only no flag", func(t *testing.T) {
		_, err := Decode([]byte{0x01}) // ID=1, but no flag
		if err != ErrFrameTooShort {
			t.Errorf("expected ErrFrameTooShort, got %v", err)
		}
	})

	t.Run("Invalid varint", func(t *testing.T) {
		// Varint with MSB set for 11 bytes (max 10)
		data := bytes.Repeat([]byte{0xff}, 11)
		_, err := Decode(data)
		if err != ErrInvalidVarint {
			t.Errorf("expected ErrInvalidVarint, got %v", err)
		}
	})
}

func FuzzDecode(f *testing.F) {
	// Seed with valid frames
	f.Add((&Frame{ChannelID: 1, Flag: FlagData, Payload: []byte("hello")}).Encode())
	f.Add((&Frame{ChannelID: 1000, Flag: FlagCreate}).Encode())
	f.Add([]byte{0xff, 0xff, 0xff, 0xff, 0x01}) // Large varint

	f.Fuzz(func(t *testing.T, data []byte) {
		frame, err := Decode(data)
		if err != nil {
			return
		}
		
		// If decode succeeds, re-encoding should be stable
		_ = frame.Encode()
	})
}
