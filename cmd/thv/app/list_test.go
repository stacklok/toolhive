package app

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/stacklok/toolhive/pkg/core"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

func TestGetEffectiveTransportType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		workload          core.Workload
		expectedTransport string
	}{
		{
			name: "stdio transport with sse proxy mode should return sse",
			workload: core.Workload{
				TransportType: types.TransportTypeStdio,
				ProxyMode:     "sse",
			},
			expectedTransport: "sse",
		},
		{
			name: "stdio transport with streamable-http proxy mode should return streamable-http",
			workload: core.Workload{
				TransportType: types.TransportTypeStdio,
				ProxyMode:     "streamable-http",
			},
			expectedTransport: "streamable-http",
		},
		{
			name: "stdio transport with empty proxy mode should return stdio",
			workload: core.Workload{
				TransportType: types.TransportTypeStdio,
				ProxyMode:     "",
			},
			expectedTransport: "stdio",
		},
		{
			name: "sse transport should return sse regardless of proxy mode",
			workload: core.Workload{
				TransportType: types.TransportTypeSSE,
				ProxyMode:     "streamable-http", // This shouldn't matter for non-stdio
			},
			expectedTransport: "sse",
		},
		{
			name: "streamable-http transport should return streamable-http",
			workload: core.Workload{
				TransportType: types.TransportTypeStreamableHTTP,
				ProxyMode:     "",
			},
			expectedTransport: "streamable-http",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := getEffectiveTransportType(tt.workload)
			assert.Equal(t, tt.expectedTransport, result, "Effective transport type should match expected")
		})
	}
}
