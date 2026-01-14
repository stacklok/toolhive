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
	"errors"
	"net/url"

	"github.com/stacklok/toolhive/pkg/networking"
)

// UserInfoFieldMapping maps provider-specific field names to standard UserInfo fields.
// This allows adapting non-standard provider responses to the canonical UserInfo structure.
type UserInfoFieldMapping struct {
	// SubjectField is the field name for the user ID (default: "sub").
	SubjectField string

	// NameField is the field name for the display name (default: "name").
	NameField string

	// EmailField is the field name for the email address (default: "email").
	EmailField string
}

// UserInfoConfig contains configuration for fetching user information from an upstream provider.
// This supports both standard OIDC UserInfo endpoints and custom provider-specific endpoints.
// Authentication is always performed using Bearer token in the Authorization header.
type UserInfoConfig struct {
	// EndpointURL is the URL of the userinfo endpoint (required).
	EndpointURL string

	// HTTPMethod is the HTTP method to use (default: GET).
	HTTPMethod string

	// AdditionalHeaders contains extra headers to include in the request.
	AdditionalHeaders map[string]string

	// FieldMapping contains custom field mapping configuration.
	// If nil, standard OIDC field names are used ("sub", "name", "email").
	FieldMapping *UserInfoFieldMapping
}

// Validate checks that UserInfoConfig has all required fields and valid values.
func (c *UserInfoConfig) Validate() error {
	if c.EndpointURL == "" {
		return errors.New("endpoint_url is required")
	}

	parsed, err := url.Parse(c.EndpointURL)
	if err != nil {
		return errors.New("endpoint_url must be a valid URL")
	}

	if parsed.Scheme == "" || parsed.Host == "" {
		return errors.New("endpoint_url must be an absolute URL with scheme and host")
	}

	if parsed.Scheme != networking.HttpScheme && parsed.Scheme != networking.HttpsScheme {
		return errors.New("endpoint_url must use http or https scheme")
	}

	return nil
}
