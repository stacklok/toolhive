// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authz

import (
	"net/http"
	"net/http/httptest"
	"strconv"
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

// TestPOSTRelabelsJSONWrittenInPlaceOfMislabeledBody pins the head of a JSON body the
// filter writes in place of a mislabeled one. The backend's Content-Length and media
// type describe the original bytes: left in place, the length truncates the shorter
// replacement and the media type mislabels it.
func TestPOSTRelabelsJSONWrittenInPlaceOfMislabeledBody(t *testing.T) {
	t.Parallel()

	deep := strings.Repeat("[", 10001) + strings.Repeat("]", 10001)
	testCases := []struct {
		name       string
		body       string
		action     sniffAction
		wantStatus int
		wantID     string
	}{
		{
			name:       "result nested past the depth cap is rejected",
			body:       `{"jsonrpc":"2.0","id":7,"result":{"tools":[{"name":"secret","inputSchema":{"x":` + deep + `}}]}}`,
			action:     sniffRejectNamed,
			wantStatus: http.StatusInternalServerError,
			wantID:     `"id":7`,
		},
		{
			name:       "result outside a clean Response is rejected",
			body:       `{"jsonrpc":"2.0","method":"notifications/message","result":{"tools":[{"name":"secret","inputSchema":{"type":"object"}}]}}`,
			action:     sniffRejectJSON,
			wantStatus: http.StatusInternalServerError,
		},
		{
			name:       "clean Response is filtered",
			body:       `{"jsonrpc":"2.0","id":9,"result":{"tools":[{"name":"secret","inputSchema":{"type":"object"}}]}}`,
			action:     sniffFilterJSON,
			wantStatus: http.StatusOK,
			wantID:     `"id":9`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.action, classifyUnlabeledBody([]byte(tc.body)), "the body must take the intended sniff route")

			authorizer := &mockAuthorizer{results: map[string]mockResult{
				"secret": {authorized: false},
			}}
			req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
			rr := httptest.NewRecorder()
			rfw := NewResponseFilteringWriter(rr, authorizer, req, "tools/list", nil, nil)
			rfw.ResponseWriter.Header().Set("Content-Type", "text/plain")
			rfw.ResponseWriter.Header().Set("Content-Length", strconv.Itoa(len(tc.body)))
			rfw.ResponseWriter.Header().Set("ETag", `"backend"`)
			_, err := rfw.Write([]byte(tc.body))
			require.NoError(t, err)
			require.NoError(t, rfw.FlushAndFilter())

			assert.Equal(t, tc.wantStatus, rr.Code)
			assert.Empty(t, rr.Header().Get("Content-Length"), "the backend's length must not describe the replacement")
			assert.Empty(t, rr.Header().Get("ETag"), "the backend's entity tag must not describe the replacement")
			assert.Equal(t, "application/json", rr.Header().Get("Content-Type"))
			assert.NotContains(t, rr.Body.String(), "secret")
			assert.Contains(t, rr.Body.String(), `"jsonrpc":"2.0"`)
			if tc.wantID != "" {
				assert.Contains(t, rr.Body.String(), tc.wantID)
			}
		})
	}
}
