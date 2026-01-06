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

package runconfig

import (
	"testing"
)

func TestResolveIssuer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		issuer    string
		proxyPort int
		want      string
		wantErr   bool
	}{
		{
			name:      "replaces port 0 with actual port",
			issuer:    "http://localhost:0/auth",
			proxyPort: 8080,
			want:      "http://localhost:8080/auth",
		},
		{
			name:      "does not replace non-zero port",
			issuer:    "http://localhost:3000",
			proxyPort: 8080,
			want:      "http://localhost:3000",
		},
		{
			name:      "does not match :0 in path",
			issuer:    "https://example.com/path:0/something",
			proxyPort: 8080,
			want:      "https://example.com/path:0/something",
		},
		{
			name:      "returns error for invalid URL",
			issuer:    "://invalid",
			proxyPort: 8080,
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := resolveIssuer(tt.issuer, tt.proxyPort)
			if (err != nil) != tt.wantErr {
				t.Errorf("resolveIssuer() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("resolveIssuer() = %v, want %v", got, tt.want)
			}
		})
	}
}
