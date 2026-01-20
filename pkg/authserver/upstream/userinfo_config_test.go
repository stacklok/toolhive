// Copyright 2025 Stacklok, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package upstream

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestUserInfoConfig_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		config  *UserInfoConfig
		wantErr string
	}{
		{
			name: "valid config with minimal fields",
			config: &UserInfoConfig{
				EndpointURL: "https://example.com/userinfo",
			},
			wantErr: "",
		},
		{
			name: "valid config with all optional fields",
			config: &UserInfoConfig{
				EndpointURL: "https://example.com/userinfo",
				HTTPMethod:  "POST",
				AdditionalHeaders: map[string]string{
					"Accept": "application/json",
				},
				FieldMapping: &UserInfoFieldMapping{
					SubjectField: "user_id",
				},
			},
			wantErr: "",
		},
		{
			name: "valid config with http localhost",
			config: &UserInfoConfig{
				EndpointURL: "http://localhost:8080/userinfo",
			},
			wantErr: "",
		},
		{
			name: "missing endpoint_url",
			config: &UserInfoConfig{
				EndpointURL: "",
			},
			wantErr: "endpoint_url is required",
		},
		{
			name: "invalid endpoint_url - not a URL",
			config: &UserInfoConfig{
				EndpointURL: "not-a-valid-url\x00",
			},
			wantErr: "endpoint_url must be a valid URL",
		},
		{
			name: "invalid endpoint_url - relative URL",
			config: &UserInfoConfig{
				EndpointURL: "/userinfo",
			},
			wantErr: "endpoint_url must be an absolute URL with scheme and host",
		},
		{
			name: "invalid endpoint_url - missing scheme",
			config: &UserInfoConfig{
				EndpointURL: "example.com/userinfo",
			},
			wantErr: "endpoint_url must be an absolute URL with scheme and host",
		},
		{
			name: "invalid endpoint_url - unsupported scheme",
			config: &UserInfoConfig{
				EndpointURL: "ftp://example.com/userinfo",
			},
			wantErr: "endpoint_url must use http or https scheme",
		},
		{
			name: "invalid endpoint_url - http to non-localhost",
			config: &UserInfoConfig{
				EndpointURL: "http://example.com/userinfo",
			},
			wantErr: "endpoint_url with http scheme requires loopback address",
		},
		{
			name: "valid config with http 127.0.0.1",
			config: &UserInfoConfig{
				EndpointURL: "http://127.0.0.1:8080/userinfo",
			},
			wantErr: "",
		},
		{
			name: "valid config with custom subject field",
			config: &UserInfoConfig{
				EndpointURL: "https://api.github.com/user",
				FieldMapping: &UserInfoFieldMapping{
					SubjectField: "id",
				},
			},
			wantErr: "",
		},
		{
			name: "valid config with GET method",
			config: &UserInfoConfig{
				EndpointURL: "https://example.com/userinfo",
				HTTPMethod:  "GET",
			},
			wantErr: "",
		},
		{
			name: "valid config with POST method",
			config: &UserInfoConfig{
				EndpointURL: "https://example.com/userinfo",
				HTTPMethod:  "POST",
			},
			wantErr: "",
		},
		{
			name: "invalid http_method",
			config: &UserInfoConfig{
				EndpointURL: "https://example.com/userinfo",
				HTTPMethod:  "DELETE",
			},
			wantErr: "http_method must be GET or POST",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.config.Validate()
			if tt.wantErr == "" {
				assert.NoError(t, err)
			} else {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
			}
		})
	}
}

func TestUserInfoFieldMapping_GetSubjectField(t *testing.T) {
	t.Parallel()

	t.Run("nil mapping returns default", func(t *testing.T) {
		t.Parallel()
		var m *UserInfoFieldMapping
		assert.Equal(t, DefaultSubjectField, m.GetSubjectField())
	})

	t.Run("empty mapping returns default", func(t *testing.T) {
		t.Parallel()
		m := &UserInfoFieldMapping{}
		assert.Equal(t, DefaultSubjectField, m.GetSubjectField())
	})

	t.Run("configured field overrides default", func(t *testing.T) {
		t.Parallel()
		m := &UserInfoFieldMapping{
			SubjectField: "user_id",
		}
		assert.Equal(t, "user_id", m.GetSubjectField())
	})
}

func TestUserInfoFieldMapping_ResolveSubject(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		mapping     *UserInfoFieldMapping
		claims      map[string]any
		expected    string
		expectError bool
		errorMsg    string
	}{
		{
			name:     "nil mapping uses default sub field",
			mapping:  nil,
			claims:   map[string]any{"sub": "user123"},
			expected: "user123",
		},
		{
			name:     "empty mapping uses default sub field",
			mapping:  &UserInfoFieldMapping{},
			claims:   map[string]any{"sub": "user123"},
			expected: "user123",
		},
		{
			name: "custom subject field",
			mapping: &UserInfoFieldMapping{
				SubjectField: "user_id",
			},
			claims:   map[string]any{"user_id": "custom123"},
			expected: "custom123",
		},
		{
			name:        "missing subject returns error",
			mapping:     &UserInfoFieldMapping{SubjectField: "user_id"},
			claims:      map[string]any{"other": "value"},
			expectError: true,
			errorMsg:    "subject claim not found",
		},
		{
			name:        "nil mapping with missing sub returns error",
			mapping:     nil,
			claims:      map[string]any{"id": "456"},
			expectError: true,
			errorMsg:    "subject claim not found",
		},
		{
			name:        "empty string subject returns error",
			mapping:     nil,
			claims:      map[string]any{"sub": ""},
			expectError: true,
			errorMsg:    "subject claim \"sub\" is empty",
		},
		{
			name:     "numeric float64 converted to string",
			mapping:  &UserInfoFieldMapping{SubjectField: "id"},
			claims:   map[string]any{"id": float64(12345)},
			expected: "12345",
		},
		{
			name:        "unsupported type returns error",
			mapping:     nil,
			claims:      map[string]any{"sub": true},
			expectError: true,
			errorMsg:    "unsupported type",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result, err := tt.mapping.ResolveSubject(tt.claims)
			if tt.expectError {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorMsg)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}
