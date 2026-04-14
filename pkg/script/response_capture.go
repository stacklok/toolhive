// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package script

import (
	"bytes"
	"net/http"
)

// responseCapture is a minimal http.ResponseWriter that captures the
// response status, headers, and body for inner tool call dispatch.
// It avoids importing net/http/httptest in production code.
type responseCapture struct {
	status int
	header http.Header
	body   bytes.Buffer
}

func newResponseCapture() *responseCapture {
	return &responseCapture{
		status: http.StatusOK,
		header: make(http.Header),
	}
}

func (rc *responseCapture) Header() http.Header {
	return rc.header
}

func (rc *responseCapture) Write(b []byte) (int, error) {
	return rc.body.Write(b)
}

func (rc *responseCapture) WriteHeader(statusCode int) {
	rc.status = statusCode
}
