// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNormalizeIssuer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in   string
		want string
	}{
		{in: "https://as2.example.com", want: "https://as2.example.com"},
		{in: "HTTPS://AS2.Example.COM", want: "https://as2.example.com"},
		{in: "https://as2.example.com/", want: "https://as2.example.com"},
		{in: "https://as2.example.com:443", want: "https://as2.example.com"},
		{in: "https://as2.example.com:8443", want: "https://as2.example.com:8443"},
		{in: "http://localhost:80/", want: "http://localhost"},
		{in: "http://localhost:443", want: "http://localhost:443"},
		{in: "https://as2.example.com/Tenant/", want: "https://as2.example.com/Tenant"},
		{in: "https://as2.example.com/tenant//", want: "https://as2.example.com/tenant/"},
		{in: "https://[::1]:443/", want: "https://[::1]"},
		{in: "https://[::1]:8443", want: "https://[::1]:8443"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, NormalizeIssuer(tt.in))
		})
	}
}
