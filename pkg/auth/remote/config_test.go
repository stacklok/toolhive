package remote

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/stacklok/toolhive/pkg/logger"
)

func TestDeriveResourceIndicator(t *testing.T) {
	t.Parallel()
	logger.Initialize()

	tests := []struct {
		name             string
		remoteServerURL  string
		expectedResource string
	}{
		{
			name:             "valid remote URL - derive and normalize",
			remoteServerURL:  "https://MCP.Example.COM/api#fragment",
			expectedResource: "https://mcp.example.com/api",
		},
		{
			name:             "remote URL with trailing slash - preserve it",
			remoteServerURL:  "https://mcp.example.com/api/",
			expectedResource: "https://mcp.example.com/api/",
		},
		{
			name:             "remote URL with port - preserve port",
			remoteServerURL:  "https://mcp.example.com:8443/api",
			expectedResource: "https://mcp.example.com:8443/api",
		},
		{
			name:             "empty remote URL - return empty",
			remoteServerURL:  "",
			expectedResource: "",
		},
		{
			name:             "invalid remote URL - return empty",
			remoteServerURL:  "ht!tp://invalid",
			expectedResource: "",
		},
		{
			name:             "derived resource with query params - preserve them",
			remoteServerURL:  "https://mcp.example.com/api?token=abc123",
			expectedResource: "https://mcp.example.com/api?token=abc123",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := DefaultResourceIndicator(tt.remoteServerURL)
			assert.Equal(t, tt.expectedResource, got)
		})
	}
}
