package types

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/labels"
	"github.com/stacklok/toolhive/pkg/state"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

func TestWorkloadFromContainerInfo(t *testing.T) {
	ctx := context.Background()

	// Create a temporary directory for XDG_STATE_HOME
	tmpBase := t.TempDir()
	t.Setenv("XDG_STATE_HOME", tmpBase)

	// Initialize the run config store
	store, err := state.NewRunConfigStore(state.DefaultAppName)
	require.NoError(t, err)

	tests := []struct {
		name               string
		containerLabels    map[string]string
		runConfigProxyMode string
		expectedTransport  types.TransportType
		expectedProxyMode  string
	}{
		{
			name: "stdio transport with streamable-http proxy mode",
			containerLabels: map[string]string{
				labels.LabelBaseName:  "test-workload",
				labels.LabelTransport: "stdio", // Corrected label
				labels.LabelPort:      "8080",
			},
			runConfigProxyMode: "streamable-http",
			expectedTransport:  types.TransportTypeStdio,
			expectedProxyMode:  "streamable-http",
		},
		{
			name: "stdio transport with sse proxy mode",
			containerLabels: map[string]string{
				labels.LabelBaseName:  "test-workload-sse",
				labels.LabelTransport: "stdio", // Corrected label
				labels.LabelPort:      "8080",
			},
			runConfigProxyMode: "sse",
			expectedTransport:  types.TransportTypeStdio,
			expectedProxyMode:  "sse",
		},
		{
			name: "direct sse transport",
			containerLabels: map[string]string{
				labels.LabelBaseName:  "test-workload-direct",
				labels.LabelTransport: "sse",
				labels.LabelPort:      "8080",
			},
			runConfigProxyMode: "",
			expectedTransport:  types.TransportTypeSSE,
			expectedProxyMode:  "sse",
		},
	}

	//nolint:paralleltest // t.Setenv is incompatible with t.Parallel
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			workloadName := tt.containerLabels[labels.LabelBaseName]

			// Create run config with proxy mode
			config := minimalRunConfig{
				ProxyMode: tt.runConfigProxyMode,
			}
			data, err := json.Marshal(config)
			require.NoError(t, err)

			writer, err := store.GetWriter(ctx, workloadName)
			require.NoError(t, err)
			_, err = writer.Write(data)
			require.NoError(t, err)
			err = writer.Close()
			require.NoError(t, err)

			container := &runtime.ContainerInfo{
				Name:    workloadName,
				Image:   "test-image",
				State:   runtime.WorkloadStatusRunning,
				Created: time.Now(),
				Labels:  tt.containerLabels,
			}

			workload, err := WorkloadFromContainerInfo(container)
			require.NoError(t, err)

			assert.Equal(t, tt.expectedTransport, workload.TransportType, "Transport type should match expected")
			assert.Equal(t, tt.expectedProxyMode, workload.ProxyMode, "Proxy mode should match expected")
		})
	}
}
