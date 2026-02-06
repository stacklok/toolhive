// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

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
			name: "valid HTTP config with MPE claim mapping",
			config: &ConfigOptions{
				HTTP: &ConnectionConfig{
					URL: "http://localhost:9000",
				},
				ClaimMapping: "mpe",
			},
			wantErr: false,
		},
		{
			name: "valid HTTP config with standard claim mapping",
			config: &ConfigOptions{
				HTTP: &ConnectionConfig{
					URL: "http://localhost:9000",
				},
				ClaimMapping: "standard",
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
				ClaimMapping: "mpe",
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
				HTTP:         &ConnectionConfig{},
				ClaimMapping: "mpe",
			},
			wantErr: true,
			errMsg:  "http.url is required",
		},
		{
			name: "missing claim_mapping",
			config: &ConfigOptions{
				HTTP: &ConnectionConfig{
					URL: "http://localhost:9000",
				},
			},
			wantErr: true,
			errMsg:  "claim_mapping is required",
		},
		{
			name: "invalid claim_mapping",
			config: &ConfigOptions{
				HTTP: &ConnectionConfig{
					URL: "http://localhost:9000",
				},
				ClaimMapping: "invalid",
			},
			wantErr: true,
			errMsg:  "invalid claim_mapping",
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

func TestConfigOptions_GetClaimMapping(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		config *ConfigOptions
		want   string
	}{
		{
			name: "mpe mapping",
			config: &ConfigOptions{
				ClaimMapping: "mpe",
			},
			want: "mpe",
		},
		{
			name: "standard mapping",
			config: &ConfigOptions{
				ClaimMapping: "standard",
			},
			want: "standard",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := tt.config.GetClaimMapping()
			if got != tt.want {
				t.Errorf("GetClaimMapping() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestConfigOptions_CreateClaimMapper(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		config      *ConfigOptions
		wantType    string
		wantErr     bool
		errContains string
	}{
		{
			name: "MPE mapper",
			config: &ConfigOptions{
				ClaimMapping: "mpe",
			},
			wantType: "*http.MPEClaimMapper",
			wantErr:  false,
		},
		{
			name: "standard mapper",
			config: &ConfigOptions{
				ClaimMapping: "standard",
			},
			wantType: "*http.StandardClaimMapper",
			wantErr:  false,
		},
		{
			name: "invalid mapper",
			config: &ConfigOptions{
				ClaimMapping: "invalid",
			},
			wantErr:     true,
			errContains: "unknown claim mapping type",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mapper, err := tt.config.CreateClaimMapper()
			if (err != nil) != tt.wantErr {
				t.Errorf("CreateClaimMapper() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if tt.wantErr {
				if !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("CreateClaimMapper() error = %v, want error containing %q", err, tt.errContains)
				}
				return
			}

			// Check mapper type using type assertion
			switch tt.wantType {
			case "*http.MPEClaimMapper":
				if _, ok := mapper.(*MPEClaimMapper); !ok {
					t.Errorf("CreateClaimMapper() returned %T, want *MPEClaimMapper", mapper)
				}
			case "*http.StandardClaimMapper":
				if _, ok := mapper.(*StandardClaimMapper); !ok {
					t.Errorf("CreateClaimMapper() returned %T, want *StandardClaimMapper", mapper)
				}
			default:
				t.Errorf("Unknown wantType: %s", tt.wantType)
			}
		})
	}
}
