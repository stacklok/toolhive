// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package transparent

import (
	"crypto/tls"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWithTLSConfigClonesConfiguration(t *testing.T) {
	t.Parallel()

	original := &tls.Config{
		MinVersion: tls.VersionTLS12,
		ServerName: "proxy.example.test",
	}
	proxy := NewTransparentProxyWithOptions(
		"127.0.0.1", 0, "", nil, nil, nil, false, false, "sse", nil, nil, "", false, nil,
		WithTLSConfig(original),
	)
	require.NotNil(t, proxy.tlsConfig)
	assert.NotSame(t, original, proxy.tlsConfig)
	assert.Equal(t, original.MinVersion, proxy.tlsConfig.MinVersion)
	assert.Equal(t, original.ServerName, proxy.tlsConfig.ServerName)

	original.MinVersion = tls.VersionTLS13
	original.ServerName = "changed.example.test"
	assert.Equal(t, uint16(tls.VersionTLS12), proxy.tlsConfig.MinVersion)
	assert.Equal(t, "proxy.example.test", proxy.tlsConfig.ServerName)
}
