// Copyright 2023 Sean Sullivan.
// SPDX-License-Identifier: MIT

//go:build long

// This file implements a FaultInjectedConn and associated dialer to

// simulate network-level failures for chaos testing.
package multiplex

import (
	"context"
	"net"
	"net/http"
)

// ChaosDialer is a test helper that dials using the FaultInjectedConn.
// It allows us to simulate low-level TCP issues while using the high-level WebSocket API.
type ChaosDialer struct {
	Dialer
	DropRate float64
}

func (d *ChaosDialer) Dial(ctx context.Context, url string, requestHeader http.Header) (*Conn, *http.Response, error) {
	// Custom NetDial for the underlying gorilla dialer
	d.Dialer.Dialer.NetDial = func(network, addr string) (net.Conn, error) {
		conn, err := (&net.Dialer{}).DialContext(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		return NewFaultInjectedConn(conn, d.DropRate), nil
	}
	return d.Dialer.Dial(ctx, url, requestHeader)
}
