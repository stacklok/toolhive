// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authz

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPOSTFailsClosedOnSplitResultInMislabeledBody(t *testing.T) {
	t.Parallel()

	authorizer := &mockAuthorizer{results: map[string]mockResult{
		"secret": {authorized: false},
	}}
	body := "data: {\"jsonrpc\":\"2.0\",\"id\":33,\ndata: \"result\":{\"tools\":[{\"name\":\"secret\",\"inputSchema\":{\"type\":\"object\"}}]}}\n\n"
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	rr := httptest.NewRecorder()
	rfw := NewResponseFilteringWriter(rr, authorizer, req, "tools/list", nil, nil)
	rfw.ResponseWriter.Header().Set("Content-Type", "text/plain")
	_, err := rfw.Write([]byte(body))
	require.NoError(t, err)
	require.NoError(t, rfw.FlushAndFilter())

	assert.False(t, strings.Contains(rr.Body.String(), "secret"), "a split result in a mislabeled POST body must not leak")
}
