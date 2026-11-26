// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package spiffeauth

import (
	"crypto/x509"
	"net/url"
	"testing"

	"github.com/spiffe/go-spiffe/v2/spiffeid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSPIFFEIDFromCertificate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		uri        string
		wantID     spiffeid.ID
		wantErr    bool
		wantErrMsg string
	}{
		{
			name:   "returns ID for valid leaf URI",
			uri:    "spiffe://example.org/ns/default/agent",
			wantID: spiffeid.RequireFromString("spiffe://example.org/ns/default/agent"),
		},
		{
			name:       "rejects root SPIFFE ID",
			uri:        "spiffe://example.org",
			wantErr:    true,
			wantErrMsg: "SPIFFE ID path is invalid",
		},
		{
			name:    "propagates go-spiffe parse error",
			uri:     "spiffe://example.org/workload/../other",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cert := &x509.Certificate{URIs: []*url.URL{mustParseURI(t, tt.uri)}}
			id, err := SPIFFEIDFromCertificate(cert)
			if tt.wantErr {
				require.Error(t, err)
				if tt.wantErrMsg != "" {
					assert.Contains(t, err.Error(), tt.wantErrMsg)
				}
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantID, id)
		})
	}
}

func mustParseURI(t *testing.T, rawURI string) *url.URL {
	t.Helper()

	uri, err := url.Parse(rawURI)
	require.NoError(t, err)
	return uri
}
