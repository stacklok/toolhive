// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package consentpolicy

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		clientIDs         []string
		authorizationBase string
		issuer            string
		upstreamCount     int
		wantErr           string
	}{
		{name: "empty list preserves existing behavior"},
		{name: "opaque ID allowed", clientIDs: []string{"opaque-client"}, issuer: "https://auth.example.com", upstreamCount: 1},
		{name: "regular ID allowed", clientIDs: []string{"desktop-client"}, issuer: "https://auth.example.com", upstreamCount: 1},
		{name: "empty ID rejected", clientIDs: []string{""}, issuer: "https://auth.example.com", upstreamCount: 1, wantErr: "empty ID"},
		{name: "duplicate ID rejected", clientIDs: []string{"client", "client"}, issuer: "https://auth.example.com", upstreamCount: 1, wantErr: "duplicate ID"},
		{name: "absolute HTTP URL rejected", clientIDs: []string{"http://client.example.com"}, issuer: "https://auth.example.com", upstreamCount: 1, wantErr: "URL-shaped"},
		{name: "absolute CIMD URL rejected", clientIDs: []string{"https://client.example.com/metadata.json"}, issuer: "https://auth.example.com", upstreamCount: 1, wantErr: "URL-shaped"},
		{name: "opaque URI rejected as URL-shaped", clientIDs: []string{"custom:opaque-client"}, issuer: "https://auth.example.com", upstreamCount: 1, wantErr: "URL-shaped"},
		{name: "network path with authority rejected", clientIDs: []string{"//client.example.com/id"}, issuer: "https://auth.example.com", upstreamCount: 1, wantErr: "URL-shaped"},
		{name: "requires upstream", clientIDs: []string{"client"}, issuer: "https://auth.example.com", wantErr: "at least one configured upstream"},
		{name: "requires HTTPS issuer", clientIDs: []string{"client"}, issuer: "http://auth.example.com", upstreamCount: 1, wantErr: "HTTPS"},
		{name: "insecure HTTP issuer still rejected", clientIDs: []string{"client"}, issuer: "http://auth.example.com", upstreamCount: 1, wantErr: "HTTPS"},
		{name: "override is effective base", clientIDs: []string{"client"}, authorizationBase: "http://login.example.com", issuer: "https://auth.example.com", upstreamCount: 1, wantErr: "HTTPS"},
		{name: "HTTPS override permits non-HTTPS issuer", clientIDs: []string{"client"}, authorizationBase: "https://login.example.com", issuer: "http://auth.example.com", upstreamCount: 1},
		{name: "missing HTTPS authority rejected", clientIDs: []string{"client"}, issuer: "https:opaque", upstreamCount: 1, wantErr: "HTTPS"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := Validate(tt.clientIDs, tt.authorizationBase, tt.issuer, tt.upstreamCount)
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}
