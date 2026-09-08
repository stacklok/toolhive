// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package identitytoken

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCheckTransport pins which destinations may receive an identity token.
// The token is redeemable at Fulcio for a certificate in the caller's name,
// so a remote plaintext destination must be refused rather than warned about.
func TestCheckTransport(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		baseURL string
		wantErr bool
	}{
		// The default CLI target and the discovery-file form.
		{name: "loopback IPv4", baseURL: "http://127.0.0.1:8080"},
		{name: "loopback IPv6", baseURL: "http://[::1]:8080"},
		// Unix socket and named pipe transports both synthesize this.
		{name: "localhost by name", baseURL: "http://localhost"},
		{name: "localhost subdomain", baseURL: "http://thv.localhost:8080"},
		{name: "https remote", baseURL: "https://api.example.com"},
		{name: "unix scheme", baseURL: "unix:///var/run/thv.sock"},

		{name: "plaintext remote host", baseURL: "http://api.example.com", wantErr: true},
		{name: "plaintext remote IP", baseURL: "http://203.0.113.7:8080", wantErr: true},
		// A private address is still off-machine: anything on the path sees it.
		{name: "plaintext private IP", baseURL: "http://10.0.0.5:8080", wantErr: true},
		{name: "no host", baseURL: "http://", wantErr: true},
		{name: "unsupported scheme", baseURL: "ftp://example.com", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := CheckTransport(tt.baseURL)
			if !tt.wantErr {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrInsecureTransport)
		})
	}
}
