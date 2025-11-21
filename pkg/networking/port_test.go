package networking_test

import (
	"testing"

	"github.com/stacklok/toolhive/pkg/networking"
)

func TestValidateCallbackPort(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		port      int
		clientID  string
		wantError bool
		errorMsg  string
	}{
		{
			name:      "valid port with client ID",
			port:      8090,
			clientID:  "test-client",
			wantError: false,
		},
		{
			name:      "valid port without client ID",
			port:      8090,
			clientID:  "",
			wantError: false,
		},
		{
			name:      "port zero is allowed (dynamic allocation)",
			port:      0,
			clientID:  "test-client",
			wantError: false,
		},
		{
			name:      "negative port is not allowed",
			port:      -1,
			clientID:  "",
			wantError: true,
			errorMsg:  "OAuth callback port must be between 1 and 65535, got: -1",
		},
		{
			name:      "port too large",
			port:      123456778,
			clientID:  "",
			wantError: true,
			errorMsg:  "OAuth callback port must be between 1 and 65535, got: 123456778",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := networking.ValidateCallbackPort(tt.port, tt.clientID)

			if tt.wantError {
				if err == nil {
					t.Errorf("ValidateCallbackPort() expected error but got nil")
				} else if tt.errorMsg != "" && err.Error() != tt.errorMsg {
					t.Errorf("ValidateCallbackPort() error = %v, want %v", err.Error(), tt.errorMsg)
				}
			} else {
				if err != nil {
					t.Errorf("ValidateCallbackPort() unexpected error = %v", err)
				}
			}
		})
	}
}
