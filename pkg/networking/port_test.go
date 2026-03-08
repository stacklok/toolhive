// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package networking_test

import (
	"testing"

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
				require.Error(t, err)
				if tt.errorMsg != "" {
					require.EqualError(t, err, tt.errorMsg)
				}
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestParsePortSpec(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		portSpec          string
		expectedHostPort  string
		expectedContainer int
		wantError         bool
	}{
		{
			name:              "host:container",
			portSpec:          "8003:8001",
			expectedHostPort:  "8003",
			expectedContainer: 8001,
			wantError:         false,
		},
		{
			name:              "container only",
			portSpec:          "8001",
			expectedHostPort:  "", // Random
			expectedContainer: 8001,
			wantError:         false,
		},
		{
			name:              "invalid format",
			portSpec:          "invalid",
			expectedHostPort:  "",
			expectedContainer: 0,
			wantError:         true,
		},
		{
			name:              "invalid host port",
			portSpec:          "abc:8001",
			expectedHostPort:  "",
			expectedContainer: 0,
			wantError:         true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			hostPort, containerPort, err := networking.ParsePortSpec(tt.portSpec)

			if tt.wantError {
				require.Error(t, err, "ParsePortSpec(%s) expected error", tt.portSpec)
				return
			}

			require.NoError(t, err, "ParsePortSpec(%s) unexpected error", tt.portSpec)

			if tt.expectedHostPort != "" {
				require.Equal(t, tt.expectedHostPort, hostPort, "ParsePortSpec(%s) unexpected host port", tt.portSpec)
			} else {
				require.NotEmpty(t, hostPort, "ParsePortSpec(%s) hostPort is empty, want random port", tt.portSpec)
			}

			require.Equal(t, tt.expectedContainer, containerPort, "ParsePortSpec(%s) unexpected container port", tt.portSpec)
		})
	}
}
