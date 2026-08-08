// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package externalauthsupport

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
)

func TestExternalAuthTypeSupport(t *testing.T) {
	t.Parallel()

	authTypes := []mcpv1beta1.ExternalAuthType{
		mcpv1beta1.ExternalAuthTypeTokenExchange,
		mcpv1beta1.ExternalAuthTypeHeaderInjection,
		mcpv1beta1.ExternalAuthTypeBearerToken,
		mcpv1beta1.ExternalAuthTypeUnauthenticated,
		mcpv1beta1.ExternalAuthTypeEmbeddedAuthServer,
		mcpv1beta1.ExternalAuthTypeAWSSts,
		mcpv1beta1.ExternalAuthTypeUpstreamInject,
		mcpv1beta1.ExternalAuthTypeOBO,
		mcpv1beta1.ExternalAuthTypeXAA,
	}

	tests := []struct {
		consumer  Consumer
		supported map[mcpv1beta1.ExternalAuthType]bool
	}{
		{
			consumer: ConsumerMCPServer,
			supported: map[mcpv1beta1.ExternalAuthType]bool{
				mcpv1beta1.ExternalAuthTypeTokenExchange:      true,
				mcpv1beta1.ExternalAuthTypeUnauthenticated:    true,
				mcpv1beta1.ExternalAuthTypeEmbeddedAuthServer: true,
				mcpv1beta1.ExternalAuthTypeOBO:                true,
			},
		},
		{
			consumer: ConsumerMCPRemoteProxy,
			supported: map[mcpv1beta1.ExternalAuthType]bool{
				mcpv1beta1.ExternalAuthTypeTokenExchange:      true,
				mcpv1beta1.ExternalAuthTypeBearerToken:        true,
				mcpv1beta1.ExternalAuthTypeUnauthenticated:    true,
				mcpv1beta1.ExternalAuthTypeEmbeddedAuthServer: true,
				mcpv1beta1.ExternalAuthTypeAWSSts:             true,
				mcpv1beta1.ExternalAuthTypeOBO:                true,
			},
		},
		{
			consumer: ConsumerVirtualMCPServer,
			supported: map[mcpv1beta1.ExternalAuthType]bool{
				mcpv1beta1.ExternalAuthTypeTokenExchange:   true,
				mcpv1beta1.ExternalAuthTypeHeaderInjection: true,
				mcpv1beta1.ExternalAuthTypeUnauthenticated: true,
				mcpv1beta1.ExternalAuthTypeAWSSts:          true,
				mcpv1beta1.ExternalAuthTypeUpstreamInject:  true,
				mcpv1beta1.ExternalAuthTypeOBO:             true,
				mcpv1beta1.ExternalAuthTypeXAA:             true,
			},
		},
		{
			consumer: ConsumerMCPServerEntry,
			supported: map[mcpv1beta1.ExternalAuthType]bool{
				mcpv1beta1.ExternalAuthTypeTokenExchange:   true,
				mcpv1beta1.ExternalAuthTypeHeaderInjection: true,
				mcpv1beta1.ExternalAuthTypeUnauthenticated: true,
				mcpv1beta1.ExternalAuthTypeAWSSts:          true,
				mcpv1beta1.ExternalAuthTypeUpstreamInject:  true,
				mcpv1beta1.ExternalAuthTypeOBO:             true,
				mcpv1beta1.ExternalAuthTypeXAA:             true,
			},
		},
	}

	for _, tt := range tests {
		t.Run(string(tt.consumer), func(t *testing.T) {
			t.Parallel()

			for _, authType := range authTypes {
				t.Run(string(authType), func(t *testing.T) {
					t.Parallel()

					wantSupported := tt.supported[authType]
					assert.Equal(t, wantSupported, Supports(tt.consumer, authType))

					err := Validate(tt.consumer, authType)
					if wantSupported {
						require.NoError(t, err)
						return
					}

					require.Error(t, err)
					var unsupportedErr *UnsupportedTypeError
					require.True(t, errors.As(err, &unsupportedErr))
					assert.Equal(t, tt.consumer, unsupportedErr.Consumer)
					assert.Equal(t, authType, unsupportedErr.AuthType)
					assert.Contains(t, err.Error(), string(tt.consumer))
					assert.Contains(t, err.Error(), string(authType))
				})
			}
		})
	}
}

func TestExternalAuthTypeSupportUnknownConsumer(t *testing.T) {
	t.Parallel()

	const unknown Consumer = "Unknown"
	assert.False(t, Supports(unknown, mcpv1beta1.ExternalAuthTypeTokenExchange))

	err := Validate(unknown, mcpv1beta1.ExternalAuthTypeTokenExchange)
	require.Error(t, err)
	var unsupportedErr *UnsupportedTypeError
	require.True(t, errors.As(err, &unsupportedErr))
	assert.Equal(t, unknown, unsupportedErr.Consumer)
	assert.Equal(t, mcpv1beta1.ExternalAuthTypeTokenExchange, unsupportedErr.AuthType)
}
