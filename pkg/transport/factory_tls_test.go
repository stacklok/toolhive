// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package transport

import (
	"crypto/tls"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/transport/types"
)

func TestFactoryCreateTLSConfig(t *testing.T) {
	t.Parallel()

	factory := NewFactory()
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}

	for _, transportType := range []types.TransportType{
		types.TransportTypeStdio,
		types.TransportTypeSSE,
		types.TransportTypeStreamableHTTP,
	} {
		t.Run(transportType.String()+" propagates TLS configuration", func(t *testing.T) {
			t.Parallel()

			created, err := factory.Create(types.Config{
				Type:      transportType,
				TLSConfig: tlsConfig,
			})
			require.NoError(t, err)

			switch typed := created.(type) {
			case *StdioTransport:
				assert.Same(t, tlsConfig, typed.tlsConfig)
			case *HTTPTransport:
				assert.Same(t, tlsConfig, typed.tlsConfig)
			default:
				require.Failf(t, "unexpected transport type", "got %T", created)
			}
		})
	}
}
