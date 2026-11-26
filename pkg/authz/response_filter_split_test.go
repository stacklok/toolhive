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

func TestPOSTFiltersSplitResultInMislabeledBody(t *testing.T) {
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
	assert.Contains(t, rr.Body.String(), `"id":33`, "the split result must be filtered and answered, not dropped")
}

// TestPOSTFiltersNestedPastDepthCapInUndecodableBody pins the POST path to the GET
// stream's rule: a result nested past encoding/json's depth cap is failed closed, not
// written raw, under both a JSON and a mislabeled media type.
func TestPOSTFiltersNestedPastDepthCapInUndecodableBody(t *testing.T) {
	t.Parallel()

	deep := strings.Repeat("[", 10001) + strings.Repeat("]", 10001)
	body := `{"jsonrpc":"2.0","id":7,"result":{"tools":[{"name":"secret","inputSchema":{"x":` + deep + `}}]}}`

	for _, contentType := range []string{"application/json", "text/plain"} {
		t.Run(contentType, func(t *testing.T) {
			t.Parallel()

			authorizer := &mockAuthorizer{results: map[string]mockResult{
				"secret": {authorized: false},
			}}
			req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
			rr := httptest.NewRecorder()
			rfw := NewResponseFilteringWriter(rr, authorizer, req, "tools/list", nil, nil)
			rfw.ResponseWriter.Header().Set("Content-Type", contentType)
			_, err := rfw.Write([]byte(body))
			require.NoError(t, err)
			require.NoError(t, rfw.FlushAndFilter())

			assert.False(t, strings.Contains(rr.Body.String(), "secret"), "a deep result must not leak")
			assert.Contains(t, rr.Body.String(), `"id":7`, "the deep result must be answered with its id")
		})
	}
}
