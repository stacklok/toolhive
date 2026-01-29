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
					SubjectFields: []string{"user_id"},
					NameFields:    []string{"display_name"},
					EmailFields:   []string{"email_address"},
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
			name: "valid config with fallback field chains",
			config: &UserInfoConfig{
				EndpointURL: "https://api.github.com/user",
				FieldMapping: &UserInfoFieldMapping{
					SubjectFields: []string{"id", "login"},
					NameFields:    []string{"name", "login"},
					EmailFields:   []string{"email"},
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

func TestResolveField(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		claims   map[string]any
		fields   []string
		expected string
	}{
		{
			name:     "empty fields list returns empty",
			claims:   map[string]any{"sub": "user123"},
			fields:   []string{},
			expected: "",
		},
		{
			name:     "single field found",
			claims:   map[string]any{"sub": "user123"},
			fields:   []string{"sub"},
			expected: "user123",
		},
		{
			name:     "first field found in chain",
			claims:   map[string]any{"sub": "user123", "id": "456"},
			fields:   []string{"sub", "id"},
			expected: "user123",
		},
		{
			name:     "second field found when first missing",
			claims:   map[string]any{"id": "456"},
			fields:   []string{"sub", "id"},
			expected: "456",
		},
		{
			name:     "third field found when first two missing",
			claims:   map[string]any{"user_id": "789"},
			fields:   []string{"sub", "id", "user_id"},
			expected: "789",
		},
		{
			name:     "no field found returns empty",
			claims:   map[string]any{"other": "value"},
			fields:   []string{"sub", "id"},
			expected: "",
		},
		{
			name:     "empty string value skipped",
			claims:   map[string]any{"sub": "", "id": "456"},
			fields:   []string{"sub", "id"},
			expected: "456",
		},
		{
			name:     "numeric float64 converted to string",
			claims:   map[string]any{"id": float64(12345)},
			fields:   []string{"id"},
			expected: "12345",
		},
		{
			name:     "numeric int converted to string",
			claims:   map[string]any{"id": 12345},
			fields:   []string{"id"},
			expected: "12345",
		},
		{
			name:     "numeric int64 converted to string",
			claims:   map[string]any{"id": int64(12345)},
			fields:   []string{"id"},
			expected: "12345",
		},
		{
			name:     "non-string non-numeric skipped",
			claims:   map[string]any{"sub": true, "id": "456"},
			fields:   []string{"sub", "id"},
			expected: "456",
		},
		{
			name:     "nil claims returns empty",
			claims:   nil,
			fields:   []string{"sub"},
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := ResolveField(tt.claims, tt.fields)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestUserInfoFieldMapping_ResolveSubject(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		mapping     *UserInfoFieldMapping
		claims      map[string]any
		expected    string
		expectError bool
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
			name: "custom subject fields",
			mapping: &UserInfoFieldMapping{
				SubjectFields: []string{"user_id", "id"},
			},
			claims:   map[string]any{"user_id": "custom123"},
			expected: "custom123",
		},
		{
			name: "fallback to second field",
			mapping: &UserInfoFieldMapping{
				SubjectFields: []string{"user_id", "id"},
			},
			claims:   map[string]any{"id": "456"},
			expected: "456",
		},
		{
			name:        "no subject found returns error",
			mapping:     &UserInfoFieldMapping{SubjectFields: []string{"user_id"}},
			claims:      map[string]any{"other": "value"},
			expectError: true,
		},
		{
			name:        "nil mapping with missing sub returns error",
			mapping:     nil,
			claims:      map[string]any{"id": "456"},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result, err := tt.mapping.ResolveSubject(tt.claims)
			if tt.expectError {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), "subject claim not found")
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}

func TestUserInfoFieldMapping_ResolveName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		mapping  *UserInfoFieldMapping
		claims   map[string]any
		expected string
	}{
		{
			name:     "nil mapping uses default name field",
			mapping:  nil,
			claims:   map[string]any{"name": "John Doe"},
			expected: "John Doe",
		},
		{
			name: "custom name fields with fallback",
			mapping: &UserInfoFieldMapping{
				NameFields: []string{"display_name", "full_name", "name"},
			},
			claims:   map[string]any{"full_name": "Jane Doe"},
			expected: "Jane Doe",
		},
		{
			name:     "missing name returns empty (optional)",
			mapping:  nil,
			claims:   map[string]any{"sub": "user123"},
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := tt.mapping.ResolveName(tt.claims)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestUserInfoFieldMapping_ResolveEmail(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		mapping  *UserInfoFieldMapping
		claims   map[string]any
		expected string
	}{
		{
			name:     "nil mapping uses default email field",
			mapping:  nil,
			claims:   map[string]any{"email": "test@example.com"},
			expected: "test@example.com",
		},
		{
			name: "custom email fields with fallback",
			mapping: &UserInfoFieldMapping{
				EmailFields: []string{"email_address", "mail", "email"},
			},
			claims:   map[string]any{"mail": "user@corp.com"},
			expected: "user@corp.com",
		},
		{
			name:     "missing email returns empty (optional)",
			mapping:  nil,
			claims:   map[string]any{"sub": "user123"},
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := tt.mapping.ResolveEmail(tt.claims)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestUserInfoFieldMapping_GetFields(t *testing.T) {
	t.Parallel()

	t.Run("nil mapping returns defaults", func(t *testing.T) {
		t.Parallel()
		var m *UserInfoFieldMapping
		assert.Equal(t, DefaultSubjectFields, m.GetSubjectFields())
		assert.Equal(t, DefaultNameFields, m.GetNameFields())
		assert.Equal(t, DefaultEmailFields, m.GetEmailFields())
	})

	t.Run("empty mapping returns defaults", func(t *testing.T) {
		t.Parallel()
		m := &UserInfoFieldMapping{}
		assert.Equal(t, DefaultSubjectFields, m.GetSubjectFields())
		assert.Equal(t, DefaultNameFields, m.GetNameFields())
		assert.Equal(t, DefaultEmailFields, m.GetEmailFields())
	})

	t.Run("configured fields override defaults", func(t *testing.T) {
		t.Parallel()
		m := &UserInfoFieldMapping{
			SubjectFields: []string{"user_id", "id"},
			NameFields:    []string{"display_name"},
			EmailFields:   []string{"mail", "email"},
		}
		assert.Equal(t, []string{"user_id", "id"}, m.GetSubjectFields())
		assert.Equal(t, []string{"display_name"}, m.GetNameFields())
		assert.Equal(t, []string{"mail", "email"}, m.GetEmailFields())
	})
}
