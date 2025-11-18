package transport

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestHTTPTransport_ShouldRestart tests the ShouldRestart logic
func TestHTTPTransport_ShouldRestart(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		exitError      error
		expectedResult bool
	}{
		{
			name:           "container exited - should restart",
			exitError:      fmt.Errorf("container exited unexpectedly"),
			expectedResult: true,
		},
		{
			name:           "container removed - should not restart",
			exitError:      fmt.Errorf("Container test (test) not found, it may have been removed"),
			expectedResult: false,
		},
		{
			name:           "no error - should not restart",
			exitError:      nil,
			expectedResult: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			transport := &HTTPTransport{
				containerName:    "test-container",
				containerExitErr: tt.exitError,
			}

			result := transport.ShouldRestart()
			assert.Equal(t, tt.expectedResult, result)
		})
	}
}
