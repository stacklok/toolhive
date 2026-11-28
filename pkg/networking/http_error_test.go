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

package networking

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewHTTPError(t *testing.T) {
	t.Parallel()

	err := NewHTTPError(404, "http://example.com/api", "not found")

	require.Error(t, err)
	var httpErr *HTTPError
	require.True(t, errors.As(err, &httpErr))
	assert.Equal(t, 404, httpErr.StatusCode)
	assert.Equal(t, "http://example.com/api", httpErr.URL)
	assert.Equal(t, "not found", httpErr.Message)
}

func TestHTTPError_Error(t *testing.T) {
	t.Parallel()

	err := &HTTPError{
		StatusCode: 404,
		Message:    "not found",
		URL:        "http://example.com/api",
	}

	assert.Equal(t, "HTTP 404 for URL http://example.com/api: not found", err.Error())
}

// isHTTPError reports whether err is an HTTPError with the given status code.
// A statusCode of 0 matches any HTTPError.
func isHTTPError(err error, statusCode int) bool {
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) {
		return false
	}
	return statusCode == 0 || httpErr.StatusCode == statusCode
}
