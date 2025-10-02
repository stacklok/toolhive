package types

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/stacklok/toolhive/pkg/transport/types"
)

func TestGetEffectiveProxyMode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		transportType     types.TransportType
		proxyMode         string
		expectedProxyMode string
	}{
		{
			name:              "stdio transport with sse proxy mode should return sse",
			transportType:     types.TransportTypeStdio,
			proxyMode:         "sse",
			expectedProxyMode: "sse",
		},
		{
			name:              "stdio transport with streamable-http proxy mode should return streamable-http",
			transportType:     types.TransportTypeStdio,
			proxyMode:         "streamable-http",
			expectedProxyMode: "streamable-http",
		},
		{
			name:              "stdio transport with empty proxy mode should return empty",
			transportType:     types.TransportTypeStdio,
			proxyMode:         "",
			expectedProxyMode: "",
		},
		{
			name:              "sse transport should return sse",
			transportType:     types.TransportTypeSSE,
			proxyMode:         "",
			expectedProxyMode: "sse",
		},
		{
			name:              "streamable-http transport should return streamable-http",
			transportType:     types.TransportTypeStreamableHTTP,
			proxyMode:         "",
			expectedProxyMode: "streamable-http",
		},
		{
			name:              "sse transport ignores provided proxy mode",
			transportType:     types.TransportTypeSSE,
			proxyMode:         "some-value",
			expectedProxyMode: "sse",
		},
		{
			name:              "stdio transport with invalid proxy mode should return the invalid mode",
			transportType:     types.TransportTypeStdio,
			proxyMode:         "invalid-mode",
			expectedProxyMode: "invalid-mode",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := GetEffectiveProxyMode(tt.transportType, tt.proxyMode)
			assert.Equal(t, tt.expectedProxyMode, result, "Effective proxy mode should match expected")
		})
	}
}
