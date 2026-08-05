// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package validation_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/validation"
)

func TestValidateRemoteURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                 string
		rawURL               string
		allowPrivateEndpoint bool
		wantErr              bool
		errContains          string
	}{
		{
			name:    "valid https URL",
			rawURL:  "https://mcp.example.com",
			wantErr: false,
		},
		{
			name:    "valid http URL",
			rawURL:  "http://mcp.example.com",
			wantErr: false,
		},
		{
			name:        "empty URL",
			rawURL:      "",
			wantErr:     true,
			errContains: "empty",
		},
		{
			name:        "no scheme",
			rawURL:      "mcp.example.com",
			wantErr:     true,
			errContains: "scheme",
		},
		{
			name:        "unsupported scheme",
			rawURL:      "ftp://mcp.example.com",
			wantErr:     true,
			errContains: "scheme",
		},
		{
			name:        "missing host",
			rawURL:      "https://",
			wantErr:     true,
			errContains: "host",
		},
		// SSRF: loopback
		{
			name:        "IPv4 loopback",
			rawURL:      "http://127.0.0.1:8080/",
			wantErr:     true,
			errContains: "blocked range",
		},
		{
			name:        "IPv4 loopback other",
			rawURL:      "http://127.0.0.2/",
			wantErr:     true,
			errContains: "blocked range",
		},
		{
			name:        "IPv6 loopback",
			rawURL:      "http://[::1]:8080/",
			wantErr:     true,
			errContains: "blocked range",
		},
		// SSRF: localhost
		{
			name:        "localhost",
			rawURL:      "http://localhost:8080/",
			wantErr:     true,
			errContains: "blocked internal hostname",
		},
		{
			name:        "localhost subdomain",
			rawURL:      "http://something.localhost/",
			wantErr:     true,
			errContains: "blocked internal hostname",
		},
		// SSRF: link-local / cloud metadata
		{
			name:        "cloud metadata endpoint",
			rawURL:      "http://169.254.169.254/latest/meta-data/",
			wantErr:     true,
			errContains: "blocked range",
		},
		{
			name:        "link-local other",
			rawURL:      "http://169.254.0.1/",
			wantErr:     true,
			errContains: "blocked range",
		},
		// SSRF: RFC 1918 private ranges
		{
			name:        "private 10.x.x.x",
			rawURL:      "http://10.0.0.1/",
			wantErr:     true,
			errContains: "blocked range",
		},
		{
			name:        "private 172.16.x.x",
			rawURL:      "http://172.16.0.1/",
			wantErr:     true,
			errContains: "blocked range",
		},
		{
			name:        "private 192.168.x.x",
			rawURL:      "http://192.168.1.1/",
			wantErr:     true,
			errContains: "blocked range",
		},
		// SSRF: IPv6 link-local and ULA
		{
			name:        "IPv6 link-local",
			rawURL:      "http://[fe80::1]/",
			wantErr:     true,
			errContains: "blocked range",
		},
		{
			name:        "IPv6 ULA",
			rawURL:      "http://[fd12:3456::1]/",
			wantErr:     true,
			errContains: "blocked range",
		},
		// SSRF: IPv4-mapped IPv6 bypass prevention
		{
			name:        "IPv4-mapped IPv6 loopback",
			rawURL:      "http://[::ffff:127.0.0.1]:8080/",
			wantErr:     true,
			errContains: "blocked range",
		},
		{
			name:        "IPv4-mapped IPv6 metadata",
			rawURL:      "http://[::ffff:169.254.169.254]/",
			wantErr:     true,
			errContains: "blocked range",
		},
		// SSRF: unspecified addresses
		{
			name:        "IPv4 unspecified 0.0.0.0",
			rawURL:      "http://0.0.0.0:8080/",
			wantErr:     true,
			errContains: "blocked range",
		},
		{
			name:        "IPv6 unspecified",
			rawURL:      "http://[::]/",
			wantErr:     true,
			errContains: "blocked range",
		},
		// SSRF: K8s internal hostnames
		{
			name:        "kubernetes.default.svc",
			rawURL:      "http://kubernetes.default.svc/",
			wantErr:     true,
			errContains: "blocked internal hostname",
		},
		{
			name:        "kubernetes.default.svc.cluster.local",
			rawURL:      "http://kubernetes.default.svc.cluster.local/",
			wantErr:     true,
			errContains: "blocked internal hostname",
		},
		{
			name:        "kubernetes.default.svc subdomain",
			rawURL:      "http://api.kubernetes.default.svc/",
			wantErr:     true,
			errContains: "blocked internal hostname",
		},
		{
			name:        "kubernetes.default",
			rawURL:      "http://kubernetes.default/api",
			wantErr:     true,
			errContains: "blocked internal hostname",
		},
		{
			name:        "arbitrary cluster.local service",
			rawURL:      "http://my-svc.my-ns.svc.cluster.local/",
			wantErr:     true,
			errContains: "blocked internal hostname",
		},
		// SSRF: GCP metadata
		{
			name:        "GCP metadata hostname",
			rawURL:      "http://metadata.google.internal/computeMetadata/v1/",
			wantErr:     true,
			errContains: "blocked internal hostname",
		},
		// Valid external URLs should still pass
		{
			name:    "valid external IP",
			rawURL:  "https://203.0.113.50:443/mcp",
			wantErr: false,
		},
		{
			name:    "valid external hostname with path",
			rawURL:  "https://mcp.example.com/v1/server",
			wantErr: false,
		},
		// Edge: 172.15.x.x is NOT in the 172.16.0.0/12 range
		{
			name:    "non-private 172.15.x.x",
			rawURL:  "http://172.15.255.255/",
			wantErr: false,
		},
		// A ".svc" hostname without the cluster domain is not blocked (no DNS
		// resolution happens), with or without AllowPrivateEndpoint.
		{
			name:    ".svc hostname allowed without AllowPrivateEndpoint",
			rawURL:  "http://my-svc.my-ns.svc:8080/",
			wantErr: false,
		},
		{
			name:                 ".svc hostname allowed with AllowPrivateEndpoint",
			rawURL:               "http://my-svc.my-ns.svc:8080/",
			allowPrivateEndpoint: true,
			wantErr:              false,
		},
		// AllowPrivateEndpoint relaxes RFC 1918, ULA, and cluster.local
		{
			name:                 "private 10.x.x.x allowed with AllowPrivateEndpoint",
			rawURL:               "http://10.0.0.5:8080/",
			allowPrivateEndpoint: true,
			wantErr:              false,
		},
		{
			name:                 "private 172.16.x.x allowed with AllowPrivateEndpoint",
			rawURL:               "http://172.16.0.1/",
			allowPrivateEndpoint: true,
			wantErr:              false,
		},
		{
			name:                 "private 192.168.x.x allowed with AllowPrivateEndpoint",
			rawURL:               "http://192.168.1.1/",
			allowPrivateEndpoint: true,
			wantErr:              false,
		},
		{
			name:                 "IPv6 ULA allowed with AllowPrivateEndpoint",
			rawURL:               "http://[fd12:3456::1]/",
			allowPrivateEndpoint: true,
			wantErr:              false,
		},
		{
			name:                 "cluster.local service allowed with AllowPrivateEndpoint",
			rawURL:               "http://my-svc.my-ns.svc.cluster.local/",
			allowPrivateEndpoint: true,
			wantErr:              false,
		},
		{
			name:                 "IPv4-mapped IPv6 private allowed with AllowPrivateEndpoint",
			rawURL:               "http://[::ffff:10.0.0.5]:8080/",
			allowPrivateEndpoint: true,
			wantErr:              false,
		},
		// AllowPrivateEndpoint never relaxes loopback, link-local, unspecified,
		// or metadata endpoints
		{
			name:                 "IPv4 loopback blocked despite AllowPrivateEndpoint",
			rawURL:               "http://127.0.0.1:8080/",
			allowPrivateEndpoint: true,
			wantErr:              true,
			errContains:          "blocked range",
		},
		{
			name:                 "IPv6 loopback blocked despite AllowPrivateEndpoint",
			rawURL:               "http://[::1]:8080/",
			allowPrivateEndpoint: true,
			wantErr:              true,
			errContains:          "blocked range",
		},
		{
			name:                 "IPv4 link-local metadata blocked despite AllowPrivateEndpoint",
			rawURL:               "http://169.254.169.254/latest/meta-data/",
			allowPrivateEndpoint: true,
			wantErr:              true,
			errContains:          "blocked range",
		},
		{
			name:                 "IPv6 link-local blocked despite AllowPrivateEndpoint",
			rawURL:               "http://[fe80::1]/",
			allowPrivateEndpoint: true,
			wantErr:              true,
			errContains:          "blocked range",
		},
		{
			name:                 "IPv4 unspecified 0.0.0.0 blocked despite AllowPrivateEndpoint",
			rawURL:               "http://0.0.0.0:8080/",
			allowPrivateEndpoint: true,
			wantErr:              true,
			errContains:          "blocked range",
		},
		{
			name:                 "IPv6 unspecified blocked despite AllowPrivateEndpoint",
			rawURL:               "http://[::]/",
			allowPrivateEndpoint: true,
			wantErr:              true,
			errContains:          "blocked range",
		},
		{
			name:                 "IPv4-mapped IPv6 loopback blocked despite AllowPrivateEndpoint",
			rawURL:               "http://[::ffff:127.0.0.1]:8080/",
			allowPrivateEndpoint: true,
			wantErr:              true,
			errContains:          "blocked range",
		},
		{
			name:                 "localhost blocked despite AllowPrivateEndpoint",
			rawURL:               "http://localhost:8080/",
			allowPrivateEndpoint: true,
			wantErr:              true,
			errContains:          "blocked internal hostname",
		},
		{
			name:                 "GCP metadata hostname blocked despite AllowPrivateEndpoint",
			rawURL:               "http://metadata.google.internal/computeMetadata/v1/",
			allowPrivateEndpoint: true,
			wantErr:              true,
			errContains:          "blocked internal hostname",
		},
		{
			name:                 "kubernetes.default blocked despite AllowPrivateEndpoint",
			rawURL:               "http://kubernetes.default/api",
			allowPrivateEndpoint: true,
			wantErr:              true,
			errContains:          "blocked internal hostname",
		},
		{
			name:                 "kubernetes.default.svc blocked despite AllowPrivateEndpoint",
			rawURL:               "http://kubernetes.default.svc/",
			allowPrivateEndpoint: true,
			wantErr:              true,
			errContains:          "blocked internal hostname",
		},
		{
			name:                 "kubernetes.default.svc.cluster.local blocked despite AllowPrivateEndpoint",
			rawURL:               "http://kubernetes.default.svc.cluster.local/",
			allowPrivateEndpoint: true,
			wantErr:              true,
			errContains:          "blocked internal hostname",
		},
		{
			// Sharpest ordering case: suffix-matches both the always-blocked
			// kubernetes.default.svc.cluster.local entry and the relaxable
			// cluster.local entry; the always-blocked match must win.
			name:                 "kubernetes.default subdomain blocked despite AllowPrivateEndpoint",
			rawURL:               "http://api.kubernetes.default.svc.cluster.local/",
			allowPrivateEndpoint: true,
			wantErr:              true,
			errContains:          "blocked internal hostname",
		},
		{
			name:                 "localhost subdomain blocked despite AllowPrivateEndpoint",
			rawURL:               "http://something.localhost/",
			allowPrivateEndpoint: true,
			wantErr:              true,
			errContains:          "blocked internal hostname",
		},
		{
			name:                 "IPv4-mapped IPv6 link-local blocked despite AllowPrivateEndpoint",
			rawURL:               "http://[::ffff:169.254.169.254]/",
			allowPrivateEndpoint: true,
			wantErr:              true,
			errContains:          "blocked range",
		},
		{
			name:                 "IPv4-mapped IPv6 private blocked without AllowPrivateEndpoint",
			rawURL:               "http://[::ffff:10.0.0.5]:8080/",
			allowPrivateEndpoint: false,
			wantErr:              true,
			errContains:          "blocked range",
		},
		{
			name:                 "RFC 1918 172.16/12 upper boundary allowed with AllowPrivateEndpoint",
			rawURL:               "http://172.31.255.255/",
			allowPrivateEndpoint: true,
			wantErr:              false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := validation.ValidateRemoteURL(tt.rawURL, validation.ValidateRemoteURLOptions{
				AllowPrivateEndpoint: tt.allowPrivateEndpoint,
			})

			if tt.wantErr {
				require.Error(t, err)
				if tt.errContains != "" {
					require.Contains(t, err.Error(), tt.errContains)
				}
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestValidateJWKSURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		rawURL      string
		wantErr     bool
		errContains string
	}{
		{
			name:    "empty URL allowed",
			rawURL:  "",
			wantErr: false,
		},
		{
			name:    "valid https URL",
			rawURL:  "https://jwks.example.com/.well-known/jwks.json",
			wantErr: false,
		},
		{
			name:        "http rejected",
			rawURL:      "http://jwks.example.com",
			wantErr:     true,
			errContains: "HTTPS",
		},
		{
			name:        "unsupported scheme",
			rawURL:      "ftp://jwks.example.com",
			wantErr:     true,
			errContains: "HTTPS",
		},
		{
			name:        "missing host",
			rawURL:      "https://",
			wantErr:     true,
			errContains: "host",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := validation.ValidateJWKSURL(tt.rawURL)

			if tt.wantErr {
				require.Error(t, err)
				if tt.errContains != "" {
					require.Contains(t, err.Error(), tt.errContains)
				}
			} else {
				require.NoError(t, err)
			}
		})
	}
}
