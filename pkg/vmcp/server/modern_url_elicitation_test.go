// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mcpparser "github.com/stacklok/toolhive/pkg/mcp"
	"github.com/stacklok/toolhive/pkg/vmcp"
)

const testConnectURL = "https://connect.example.com/github?state=abc"

func testURLAnswer(t *testing.T) *vmcp.URLElicitationRequiredError {
	t.Helper()
	e, err := vmcp.NewURLElicitation("github", testConnectURL, "Reconnect GitHub to continue")
	require.NoError(t, err)
	answer, err := vmcp.NewURLElicitationRequiredError(e)
	require.NoError(t, err)
	return answer
}

func clientCapsMeta(caps map[string]any) map[string]any {
	return map[string]any{modernClientCapabilitiesKey: caps}
}

var declaredURLMeta = clientCapsMeta(map[string]any{"elicitation": map[string]any{"url": map[string]any{}}})

// runCallFailure drives the Modern call-failure renderer for method with no
// recorded refusal: writeModernToolCallFailure for tools/call, and
// writeModernCallFailure for the other call verbs. It returns the status, the
// decoded envelope, and the raw body.
func runCallFailure(
	t *testing.T, method string, meta map[string]any, err error,
) (int, map[string]any, string) {
	t.Helper()
	return runCallFailureWithID(t, method, float64(7), meta, err)
}

// runCallFailureWithID is runCallFailure with the request id as a parameter;
// a nil id is an absent one.
func runCallFailureWithID(
	t *testing.T, method string, id any, meta map[string]any, err error,
) (int, map[string]any, string) {
	t.Helper()
	_, refusal := withCapabilityRefusalRecorder(context.Background())
	parsed := &mcpparser.ParsedMCPRequest{Method: method, IsRequest: true, ID: id, Meta: meta}
	rec := httptest.NewRecorder()
	if method == "tools/call" {
		writeModernToolCallFailure(rec, parsed, refusal, "vmcp-test", "1.2.3", err)
	} else {
		writeModernCallFailure(rec, parsed, refusal, vmcp.DenyMessageToolCall, err)
	}
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &decoded), rec.Body.String())
	return rec.Code, decoded, rec.Body.String()
}

// TestWriteModernCallFailure_DenialOutranksURLElicitation pins AC2.3: an error
// that both wraps ErrAuthorizationFailed and carries the URL elicitation
// answer renders as the 403 denial, for a client that declared URL mode and
// one that did not.
func TestWriteModernCallFailure_DenialOutranksURLElicitation(t *testing.T) {
	t.Parallel()

	answer := testURLAnswer(t)
	err := fmt.Errorf("%w: %w", vmcp.ErrAuthorizationFailed, answer)
	_, ok := vmcp.URLElicitationFromError(err)
	require.True(t, ok, "the fixture must carry the answer, or the test proves nothing")

	for name, meta := range map[string]map[string]any{
		"declared url":     declaredURLMeta,
		"not declared url": clientCapsMeta(map[string]any{}),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			status, decoded, body := runCallFailure(t, "tools/call", meta, err)
			assert.Equal(t, http.StatusForbidden, status)
			errObj, _ := decoded["error"].(map[string]any)
			require.NotNil(t, errObj, body)
			assert.EqualValues(t, mcpparser.JSONRPCCodeDenied, errObj["code"])
			assert.Equal(t, vmcp.DenyMessageToolCall, errObj["message"])
			assert.NotContains(t, body, "connect.example.com")
			assert.NotContains(t, body, "input_required")
		})
	}
}

// TestWriteModernCallFailure_URLElicitationInputRequired pins the envelope
// shape AC2.1 names, at the unit level: resultType "input_required" with
// inputRequests keyed by elicitation ID, each an elicitation/create in URL
// mode carrying message and url only, no requestState, under the request's
// own id at HTTP 200.
func TestWriteModernCallFailure_URLElicitationInputRequired(t *testing.T) {
	t.Parallel()

	first, err := vmcp.NewURLElicitation("github", testConnectURL, "Reconnect GitHub")
	require.NoError(t, err)
	second, err := vmcp.NewURLElicitation("jira", "http://127.0.0.1:9000/jira", "Reconnect Jira")
	require.NoError(t, err)
	answer, err := vmcp.NewURLElicitationRequiredError(first, second)
	require.NoError(t, err)

	status, decoded, body := runCallFailure(t, "tools/call", declaredURLMeta,
		fmt.Errorf("backend github: %w", answer))
	require.Equal(t, http.StatusOK, status, body)
	assert.EqualValues(t, 7, decoded["id"])
	assert.NotContains(t, decoded, "error")

	result, _ := decoded["result"].(map[string]any)
	require.NotNil(t, result, body)
	assert.Equal(t, "input_required", result["resultType"])
	assert.NotContains(t, result, "requestState")
	meta, _ := result["_meta"].(map[string]any)
	assert.Equal(t, map[string]any{"name": "vmcp-test", "version": "1.2.3"},
		meta["io.modelcontextprotocol/serverInfo"])

	assert.Equal(t, map[string]any{
		"github": map[string]any{
			"method": "elicitation/create",
			"params": map[string]any{"mode": "url", "message": "Reconnect GitHub", "url": testConnectURL},
		},
		"jira": map[string]any{
			"method": "elicitation/create",
			"params": map[string]any{"mode": "url", "message": "Reconnect Jira", "url": "http://127.0.0.1:9000/jira"},
		},
	}, result["inputRequests"])
}

// TestWriteModernCallFailure_URLElicitationFallback pins the Modern fallback
// at the unit level: any declaration short of elicitation.url gets -32021
// naming {elicitation: {url: {}}} at HTTP 200, and no URL.
func TestWriteModernCallFailure_URLElicitationFallback(t *testing.T) {
	t.Parallel()

	answer := testURLAnswer(t)
	for name, meta := range map[string]map[string]any{
		"elicitation {}":       clientCapsMeta(map[string]any{"elicitation": map[string]any{}}),
		"form only":            clientCapsMeta(map[string]any{"elicitation": map[string]any{"form": map[string]any{}}}),
		"no elicitation":       clientCapsMeta(map[string]any{"sampling": map[string]any{}}),
		"url at the wrong key": clientCapsMeta(map[string]any{"url": map[string]any{}}),
		"no _meta":             nil,
		"url null":             clientCapsMeta(map[string]any{"elicitation": map[string]any{"url": nil}}),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			status, decoded, body := runCallFailure(t, "tools/call", meta, answer)
			require.Equal(t, http.StatusOK, status, body)
			errObj, _ := decoded["error"].(map[string]any)
			require.NotNil(t, errObj, body)
			assert.EqualValues(t, mcpparser.CodeMissingClientCapability, errObj["code"])
			assert.Contains(t, errObj["message"], "elicitation.url")
			assert.Equal(t, map[string]any{
				"requiredCapabilities": map[string]any{"elicitation": map[string]any{"url": map[string]any{}}},
			}, errObj["data"])
			assert.NotContains(t, body, "connect.example.com")
		})
	}
}

// TestWriteModernURLElicitationUnsupported_OmitsAbsentID pins the
// JSON-RPC envelope rule for the hand-built -32021 fallback: an absent request
// id omits the "id" key rather than writing "id": null.
func TestWriteModernURLElicitationUnsupported_OmitsAbsentID(t *testing.T) {
	t.Parallel()

	status, decoded, body := runCallFailureWithID(t, "tools/call", nil, clientCapsMeta(map[string]any{}), testURLAnswer(t))
	require.Equal(t, http.StatusOK, status, body)
	assert.NotContains(t, decoded, "id", body)
	errObj, _ := decoded["error"].(map[string]any)
	require.NotNil(t, errObj, body)
	assert.EqualValues(t, mcpparser.CodeMissingClientCapability, errObj["code"])
}

// TestWriteModernCallFailure_URLElicitationOnlyOnToolsCall pins the scope:
// on resources/read and prompts/get the answer renders as today's generic
// -32603, whose message is the answer's URL-free Error().
func TestWriteModernCallFailure_URLElicitationOnlyOnToolsCall(t *testing.T) {
	t.Parallel()

	answer := testURLAnswer(t)
	for _, method := range []string{"resources/read", "prompts/get"} {
		t.Run(method, func(t *testing.T) {
			t.Parallel()
			status, decoded, body := runCallFailure(t, method, declaredURLMeta, answer)
			require.Equal(t, http.StatusOK, status, body)
			errObj, _ := decoded["error"].(map[string]any)
			require.NotNil(t, errObj, body)
			assert.EqualValues(t, jsonRPCCodeInternalError, errObj["code"])
			assert.NotContains(t, body, "connect.example.com")
			assert.NotContains(t, body, "input_required")
		})
	}
}

// TestWriteModernCallFailure_BackendInputRequiredStaysInternalError pins the
// render half of AC4.2: a backend's input_required round, even one carrying a
// URL-mode elicitation/create, is not the URL elicitation answer and keeps
// rendering as -32603 with no URL. The decode half is
// TestBackendURLElicitationIsNotRendered_Modern in pkg/vmcp/client.
func TestWriteModernCallFailure_BackendInputRequiredStaysInternalError(t *testing.T) {
	t.Parallel()

	backendErr := fmt.Errorf("backend call: %w", &vmcp.InputRequiredError{
		ResultType: "input_required",
		Sentinel:   errors.New("modern response requires additional input (multi-round retrieval unsupported)"),
		Result: &vmcp.InputRequiredResult{InputRequests: map[string]json.RawMessage{
			"connect": json.RawMessage(
				`{"method":"elicitation/create","params":{"mode":"url","message":"m","url":"` + testConnectURL + `"}}`),
		}},
	})

	status, decoded, body := runCallFailure(t, "tools/call", declaredURLMeta, backendErr)
	require.Equal(t, http.StatusOK, status, body)
	errObj, _ := decoded["error"].(map[string]any)
	require.NotNil(t, errObj, body)
	assert.EqualValues(t, jsonRPCCodeInternalError, errObj["code"])
	assert.NotContains(t, decoded, "result")
	assert.NotContains(t, body, "connect.example.com")
	assert.NotContains(t, body, `"mode":"url"`)
}

func TestModernClientDeclaredURLElicitation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		meta map[string]any
		want bool
	}{
		{name: "nil meta", meta: nil, want: false},
		{name: "empty caps", meta: clientCapsMeta(map[string]any{}), want: false},
		{name: "elicitation {}", meta: clientCapsMeta(map[string]any{"elicitation": map[string]any{}}), want: false},
		{name: "form only", meta: clientCapsMeta(map[string]any{
			"elicitation": map[string]any{"form": map[string]any{}}}), want: false},
		{name: "elicitation not an object", meta: clientCapsMeta(map[string]any{"elicitation": "url"}), want: false},
		{name: "url null", meta: clientCapsMeta(map[string]any{
			"elicitation": map[string]any{"url": nil}}), want: false},
		{name: "url false", meta: clientCapsMeta(map[string]any{
			"elicitation": map[string]any{"url": false}}), want: false},
		{name: "url a string", meta: clientCapsMeta(map[string]any{
			"elicitation": map[string]any{"url": "x"}}), want: false},
		{name: "url declared", meta: declaredURLMeta, want: true},
		{name: "form and url declared", meta: clientCapsMeta(map[string]any{
			"elicitation": map[string]any{"form": map[string]any{}, "url": map[string]any{}}}), want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, modernClientDeclaredURLElicitation(tt.meta))
		})
	}
}
