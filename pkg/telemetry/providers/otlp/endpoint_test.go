// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package otlp

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSplitEndpointPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		endpoint     string
		wantHostPort string
		wantBasePath string
	}{
		{
			name:         "host and port only",
			endpoint:     "localhost:4318",
			wantHostPort: "localhost:4318",
			wantBasePath: "",
		},
		{
			name:         "hostname without port",
			endpoint:     "otel-collector.local",
			wantHostPort: "otel-collector.local",
			wantBasePath: "",
		},
		{
			name:         "Langfuse endpoint with path",
			endpoint:     "cloud.langfuse.com/api/public/otel",
			wantHostPort: "cloud.langfuse.com",
			wantBasePath: "/api/public/otel",
		},
		{
			name:         "LangSmith endpoint with port and path",
			endpoint:     "smith.langchain.com:443/api/v1/otel",
			wantHostPort: "smith.langchain.com:443",
			wantBasePath: "/api/v1/otel",
		},
		{
			name:         "trailing slash stripped",
			endpoint:     "cloud.langfuse.com/api/public/otel/",
			wantHostPort: "cloud.langfuse.com",
			wantBasePath: "/api/public/otel",
		},
		{
			name:         "empty string",
			endpoint:     "",
			wantHostPort: "",
			wantBasePath: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			hostPort, basePath := splitEndpointPath(tt.endpoint)
			assert.Equal(t, tt.wantHostPort, hostPort)
			assert.Equal(t, tt.wantBasePath, basePath)
		})
	}
}
