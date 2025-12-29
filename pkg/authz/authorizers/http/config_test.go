package http

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestConfigOptions_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		config  *ConfigOptions
		wantErr bool
		errMsg  string
	}{
		{
			name:    "nil config",
			config:  nil,
			wantErr: true,
			errMsg:  "pdp configuration is required",
		},
		{
			name: "valid HTTP config",
			config: &ConfigOptions{
				HTTP: &ConnectionConfig{
					URL: "http://localhost:9000",
				},
			},
			wantErr: false,
		},
		{
			name: "HTTP config with timeout",
			config: &ConfigOptions{
				HTTP: &ConnectionConfig{
					URL:     "https://pdp.example.com",
					Timeout: 60,
				},
			},
			wantErr: false,
		},
		{
			name:    "missing HTTP config",
			config:  &ConfigOptions{},
			wantErr: true,
			errMsg:  "http configuration is required",
		},
		{
			name: "HTTP config without URL",
			config: &ConfigOptions{
				HTTP: &ConnectionConfig{},
			},
			wantErr: true,
			errMsg:  "http.url is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.config.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr && tt.errMsg != "" {
				if err == nil || !strings.Contains(err.Error(), tt.errMsg) {
					t.Errorf("Validate() error = %v, want error containing %q", err, tt.errMsg)
				}
			}
		})
	}
}

func TestParseConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		rawConfig string
		wantErr   bool
	}{
		{
			name: "valid HTTP config",
			rawConfig: `{
				"version": "1.0",
				"type": "httpv1",
				"pdp": {
					"http": {
						"url": "http://localhost:9000"
					}
				}
			}`,
			wantErr: false,
		},
		{
			name:      "invalid JSON",
			rawConfig: `{invalid`,
			wantErr:   true,
		},
		{
			name: "missing pdp field",
			rawConfig: `{
				"version": "1.0",
				"type": "httpv1"
			}`,
			wantErr: false, // parseConfig doesn't validate, just parses
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := parseConfig(json.RawMessage(tt.rawConfig))
			if (err != nil) != tt.wantErr {
				t.Errorf("parseConfig() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
		})
	}
}
