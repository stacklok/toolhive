// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package networking_test

import (
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/networking"
)

func TestValidateCallbackPort(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		port      int
		clientID  string
		wantError bool
		errorMsg  string
	}{
		{
			name:      "valid port with client ID",
			port:      8090,
			clientID:  "test-client",
			wantError: false,
		},
		{
			name:      "valid port without client ID",
			port:      8090,
			clientID:  "",
			wantError: false,
		},
		{
			name:      "port zero is allowed (dynamic allocation)",
			port:      0,
			clientID:  "test-client",
			wantError: false,
		},
		{
			name:      "negative port is not allowed",
			port:      -1,
			clientID:  "",
			wantError: true,
			errorMsg:  "OAuth callback port must be between 1024 and 65535, got: -1",
		},
		{
			name:      "port less than 1024",
			port:      1000,
			clientID:  "",
			wantError: true,
			errorMsg:  "OAuth callback port must be between 1024 and 65535, got: 1000",
		},
		{
			name:      "port too large",
			port:      123456778,
			clientID:  "",
			wantError: true,
			errorMsg:  "OAuth callback port must be between 1024 and 65535, got: 123456778",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := networking.ValidateCallbackPort(tt.port, tt.clientID)

			if tt.wantError {
				if err == nil {
					t.Errorf("ValidateCallbackPort() expected error but got nil")
				} else if tt.errorMsg != "" && err.Error() != tt.errorMsg {
					t.Errorf("ValidateCallbackPort() error = %v, want %v", err.Error(), tt.errorMsg)
				}
			} else {
				if err != nil {
					t.Errorf("ValidateCallbackPort() unexpected error = %v", err)
				}
			}
		})
	}
}

func TestGetProcessOnPort_InvalidPort(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		port int
	}{
		{"zero port", 0},
		{"negative port", -1},
		{"port too large", 65536},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pid, err := networking.GetProcessOnPort(tt.port)
			require.Error(t, err)
			assert.Equal(t, 0, pid)
		})
	}
}

func TestGetProcessOnPort_FreePort(t *testing.T) {
	t.Parallel()

	// Use a port that FindAvailable guarantees is free
	port := networking.FindAvailable()
	require.NotZero(t, port, "FindAvailable should find a free port")

	pid, err := networking.GetProcessOnPort(port)
	require.NoError(t, err)
	assert.Equal(t, 0, pid)
}

func TestGetProcessOnPort_PortInUse(t *testing.T) {
	t.Parallel()

	// Bind to a port, then verify GetProcessOnPort returns our process
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()

	tcpAddr, ok := listener.Addr().(*net.TCPAddr)
	require.True(t, ok)
	port := tcpAddr.Port

	pid, err := networking.GetProcessOnPort(port)
	require.NoError(t, err)
	assert.NotZero(t, pid, "port is in use, GetProcessOnPort should return the process PID")
}
