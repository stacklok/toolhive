// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestBuildRemoteAuthFlowConfig_Trust covers the trust flags this function
// derives. The target URIs are IP literals so no DNS lookup is involved.
func TestBuildRemoteAuthFlowConfig_Trust(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                     string
		flags                    RemoteAuthFlags
		operatorIssuer           string
		targetURI                string
		wantIssuerTrusted        bool
		wantTokenEndpointTrusted bool
	}{
		{
			name:                     "operator-configured loopback issuer, public target",
			flags:                    RemoteAuthFlags{RemoteAuthIssuer: "http://localhost:5556/dex"},
			operatorIssuer:           "http://localhost:5556/dex",
			targetURI:                "https://93.184.216.34/mcp",
			wantIssuerTrusted:        true,
			wantTokenEndpointTrusted: true,
		},
		{
			name:                     "operator-configured token endpoint without an issuer",
			flags:                    RemoteAuthFlags{RemoteAuthTokenURL: "https://idp.example.com/token"},
			targetURI:                "https://93.184.216.34/mcp",
			wantTokenEndpointTrusted: true,
		},
		{
			name:      "endpoint named only by the remote server",
			flags:     RemoteAuthFlags{RemoteAuthClientID: "client"},
			targetURI: "https://93.184.216.34/mcp",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			flags := tt.flags
			cfg := buildRemoteAuthFlowConfig(context.Background(), &flags, "", tt.targetURI, tt.operatorIssuer)

			assert.Equal(t, tt.wantIssuerTrusted, cfg.IssuerTrusted)
			assert.Equal(t, tt.wantTokenEndpointTrusted, cfg.TokenEndpointTrusted)
			assert.False(t, cfg.AllowPrivateIPs, "a public target must not widen the discovery guard")
		})
	}
}
