package health

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestCircuitBreakerConfig_Validate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		config  *CircuitBreakerConfig
		wantErr bool
		errMsg  string
	}{
		{
			name:    "nil config is valid (disabled)",
			config:  nil,
			wantErr: false,
		},
		{
			name: "disabled config is valid",
			config: &CircuitBreakerConfig{
				Enabled:          false,
				FailureThreshold: 0, // Invalid but should be ignored when disabled
				Timeout:          0,
			},
			wantErr: false,
		},
		{
			name: "valid enabled config",
			config: &CircuitBreakerConfig{
				Enabled:          true,
				FailureThreshold: 5,
				Timeout:          60 * time.Second,
			},
			wantErr: false,
		},
		{
			name: "enabled with zero failure threshold",
			config: &CircuitBreakerConfig{
				Enabled:          true,
				FailureThreshold: 0,
				Timeout:          60 * time.Second,
			},
			wantErr: true,
			errMsg:  "circuit breaker failure threshold must be >= 1",
		},
		{
			name: "enabled with negative failure threshold",
			config: &CircuitBreakerConfig{
				Enabled:          true,
				FailureThreshold: -1,
				Timeout:          60 * time.Second,
			},
			wantErr: true,
			errMsg:  "circuit breaker failure threshold must be >= 1",
		},
		{
			name: "enabled with zero timeout",
			config: &CircuitBreakerConfig{
				Enabled:          true,
				FailureThreshold: 5,
				Timeout:          0,
			},
			wantErr: true,
			errMsg:  "circuit breaker timeout must be positive",
		},
		{
			name: "enabled with negative timeout",
			config: &CircuitBreakerConfig{
				Enabled:          true,
				FailureThreshold: 5,
				Timeout:          -1 * time.Second,
			},
			wantErr: true,
			errMsg:  "circuit breaker timeout must be positive",
		},
		{
			name: "enabled with minimum valid values",
			config: &CircuitBreakerConfig{
				Enabled:          true,
				FailureThreshold: 1,
				Timeout:          1 * time.Nanosecond,
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.config.Validate()

			if tt.wantErr {
				assert.Error(t, err, "Validate() should return error")
				if tt.errMsg != "" {
					assert.Contains(t, err.Error(), tt.errMsg, "Error message should contain expected text")
				}
			} else {
				assert.NoError(t, err, "Validate() should not return error")
			}
		})
	}
}

func TestCircuitState_String(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		state CircuitState
		want  string
	}{
		{
			name:  "closed state",
			state: CircuitClosed,
			want:  CircuitStateClosedStr,
		},
		{
			name:  "open state",
			state: CircuitOpen,
			want:  CircuitStateOpenStr,
		},
		{
			name:  "halfopen state",
			state: CircuitHalfOpen,
			want:  CircuitStateHalfOpenStr,
		},
		{
			name:  "unknown state",
			state: CircuitState(99),
			want:  CircuitStateUnknownStr,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := tt.state.String()
			assert.Equal(t, tt.want, got, "CircuitState.String() should return correct string")
		})
	}
}
