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

	t.Run("rejects TLS for stdio", func(t *testing.T) {
		t.Parallel()

		transport, err := factory.Create(types.Config{
			Type:      types.TransportTypeStdio,
			TLSConfig: tlsConfig,
		})
		require.Error(t, err)
		assert.Nil(t, transport)
	})

	for _, transportType := range []types.TransportType{
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

			httpTransport, ok := created.(*HTTPTransport)
			require.True(t, ok)
			assert.Same(t, tlsConfig, httpTransport.tlsConfig)
		})
	}
}
