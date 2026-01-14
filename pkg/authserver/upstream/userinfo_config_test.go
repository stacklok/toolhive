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
					NameField:    "display_name",
					EmailField:   "email_address",
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
