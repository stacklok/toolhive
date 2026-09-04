// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authz

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/exp/jsonrpc2"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/authz/authorizers"
	"github.com/stacklok/toolhive/pkg/authz/authorizers/cedar"
	mcpparser "github.com/stacklok/toolhive/pkg/mcp"
)

func skillRequest(t *testing.T, method, params string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/messages", bytes.NewBufferString(
		`{"jsonrpc":"2.0","id":1,"method":"`+method+`","params":`+params+`}`,
	))
	req.Header.Set("Content-Type", "application/json")
	return req.WithContext(auth.WithIdentity(context.Background(), &auth.Identity{PrincipalInfo: auth.PrincipalInfo{
		Subject: "user", Claims: map[string]interface{}{"sub": "user"},
	}}))
}

func TestMiddlewareSkillsGet(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name    string
		policy  string
		allowed bool
	}{
		{
			name:    "allowed exact URI reaches handler",
			policy:  `permit(principal, action == Action::"get_skill", resource == Skill::"mcp://example/allowed");`,
			allowed: true,
		},
		{
			name:   "denied URI does not reach handler",
			policy: `permit(principal, action == Action::"get_skill", resource == Skill::"mcp://example/other");`,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			authorizer, err := cedar.NewCedarAuthorizer(cedar.ConfigOptions{Policies: []string{tt.policy}, EntitiesJSON: `[]`}, "")
			require.NoError(t, err)

			handlerCalled := false
			next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { handlerCalled = true })
			rr := httptest.NewRecorder()
			mcpparser.ParsingMiddleware(Middleware(authorizer, next, nil)).ServeHTTP(
				rr, skillRequest(t, "skills/get", `{"uri":"mcp://example/allowed"}`),
			)

			assert.Equal(t, tt.allowed, handlerCalled)
			if !tt.allowed {
				assert.Equal(t, http.StatusForbidden, rr.Code)
			}
		})
	}
}

func TestMiddlewareDeniesSkillsGetWithDuplicateURI(t *testing.T) {
	t.Parallel()

	for _, params := range []string{
		`{"uri":"mcp://example/allowed","uri":"mcp://example/denied"}`,
		`{"uri":"mcp://example/allowed","name":"skill","uri":"mcp://example/allowed"}`,
	} {
		authorizer := &stubAuthorizer{allowed: true}
		handlerCalled := false
		rr := httptest.NewRecorder()
		mcpparser.ParsingMiddleware(Middleware(authorizer, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			handlerCalled = true
		}), nil)).ServeHTTP(rr, skillRequest(t, "skills/get", params))

		assert.Equal(t, http.StatusForbidden, rr.Code)
		assert.False(t, handlerCalled)
		assert.Zero(t, authorizer.calls)
	}
}

func TestMiddlewareDeniesSkillsGetWithoutURI(t *testing.T) {
	t.Parallel()

	for _, params := range []string{`{}`, `{"uri":""}`, `{"uri":42}`} {
		authorizer := &stubAuthorizer{allowed: true}
		handlerCalled := false
		rr := httptest.NewRecorder()
		mcpparser.ParsingMiddleware(Middleware(authorizer, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			handlerCalled = true
		}), nil)).ServeHTTP(rr, skillRequest(t, "skills/get", params))

		assert.Equal(t, http.StatusForbidden, rr.Code)
		assert.False(t, handlerCalled)
		assert.Zero(t, authorizer.calls)
	}
}

func TestMiddlewareSkillsListResponseFilter(t *testing.T) {
	t.Parallel()

	authorizer, err := cedar.NewCedarAuthorizer(cedar.ConfigOptions{
		Policies:     []string{`permit(principal, action == Action::"get_skill", resource == Skill::"mcp://example/allowed");`},
		EntitiesJSON: `[]`,
	}, "")
	require.NoError(t, err)

	response, err := jsonrpc2.EncodeMessage(&jsonrpc2.Response{ID: jsonrpc2.Int64ID(1), Result: json.RawMessage(`{
		"skills":[
			{"uri":"mcp://example/allowed","name":"allowed","manifest":{"keep":true}},
			{"uri":"mcp://example/denied","name":"denied","manifest":{"secret":true}}
		],"nextCursor":"next","extension":{"preserved":true}}`)})
	require.NoError(t, err)

	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, err := w.Write(response)
		require.NoError(t, err)
	})
	rr := httptest.NewRecorder()
	mcpparser.ParsingMiddleware(Middleware(authorizer, next, nil)).ServeHTTP(rr, skillRequest(t, "skills/list", `{}`))

	message, err := jsonrpc2.DecodeMessage(rr.Body.Bytes())
	require.NoError(t, err)
	filtered := message.(*jsonrpc2.Response)
	require.Nil(t, filtered.Error)
	assert.JSONEq(t, `{
		"skills":[{"uri":"mcp://example/allowed","name":"allowed","manifest":{"keep":true}}],
		"nextCursor":"next","extension":{"preserved":true}}`, string(filtered.Result))
}

func TestSkillsListResponseFilterFailsClosed(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name string
		body string
	}{
		{name: "malformed entry", body: `{"skills":[{"name":"missing-uri"}]}`},
		{name: "duplicate URI", body: `{"skills":[{"uri":"mcp://example/allowed","uri":"mcp://example/denied"}]}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rr := httptest.NewRecorder()
			rr.Header().Set("Content-Type", "application/json")
			writer := NewResponseFilteringWriter(rr, &stubAuthorizer{allowed: true}, httptest.NewRequest(http.MethodPost, "/messages", nil), "skills/list", nil, nil)
			body, err := jsonrpc2.EncodeMessage(&jsonrpc2.Response{ID: jsonrpc2.Int64ID(1), Result: json.RawMessage(tt.body)})
			require.NoError(t, err)
			_, err = writer.Write(body)
			require.NoError(t, err)
			require.NoError(t, writer.FlushAndFilter())

			assert.Equal(t, http.StatusInternalServerError, rr.Code)
			assert.Contains(t, rr.Body.String(), "internal error")
			assert.NotContains(t, rr.Body.String(), "missing-uri")
			assert.NotContains(t, rr.Body.String(), "mcp://example/")
		})
	}
}

func TestSkillsListResponseFilterSkipsAuthorizerErrors(t *testing.T) {
	t.Parallel()

	authorizer := &stubAuthorizer{authorize: func(feature authorizers.MCPFeature, operation authorizers.MCPOperation, id string) (bool, error) {
		if id == "mcp://example/error" {
			return false, errors.New("authorizer unavailable")
		}
		return feature == authorizers.MCPFeatureSkill && operation == authorizers.MCPOperationGet && id == "mcp://example/allowed", nil
	}}
	rr := httptest.NewRecorder()
	rr.Header().Set("Content-Type", "application/json")
	writer := NewResponseFilteringWriter(rr, authorizer, httptest.NewRequest(http.MethodPost, "/messages", nil), "skills/list", nil, nil)
	body, err := jsonrpc2.EncodeMessage(&jsonrpc2.Response{ID: jsonrpc2.Int64ID(1), Result: json.RawMessage(`{"skills":[
		{"uri":"mcp://example/allowed"},{"uri":"mcp://example/error"}]}`)})
	require.NoError(t, err)
	_, err = writer.Write(body)
	require.NoError(t, err)
	require.NoError(t, writer.FlushAndFilter())

	assert.Contains(t, rr.Body.String(), "mcp://example/allowed")
	assert.NotContains(t, rr.Body.String(), "mcp://example/error")
	assert.Equal(t, 2, authorizer.calls)
	assert.Equal(t, authorizers.MCPFeatureSkill, authorizer.lastFeature)
	assert.Equal(t, authorizers.MCPOperationGet, authorizer.lastOperation)
}

func TestSkillsListResponseFilterSSE(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name       string
		authorizer authorizers.Authorizer
		result     string
		contains   string
		omits      string
		internal   bool
	}{
		{
			name: "allowed entry",
			authorizer: mustSkillAuthorizer(t,
				`permit(principal, action == Action::"get_skill", resource == Skill::"mcp://example/allowed");`),
			result:   `{"skills":[{"uri":"mcp://example/allowed"},{"uri":"mcp://example/denied"}]}`,
			contains: `mcp://example/allowed`,
			omits:    `mcp://example/denied`,
		},
		{
			name:       "denied entries",
			authorizer: mustSkillAuthorizer(t, `permit(principal, action == Action::"get_skill", resource == Skill::"mcp://example/other");`),
			result:     `{"skills":[{"uri":"mcp://example/denied"}]}`,
			omits:      `mcp://example/denied`,
		},
		{
			name:       "malformed entry fails closed",
			authorizer: &stubAuthorizer{allowed: true},
			result:     `{"skills":[{"name":"missing-uri"}]}`,
			omits:      `missing-uri`,
			internal:   true,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			payload, err := jsonrpc2.EncodeMessage(&jsonrpc2.Response{ID: jsonrpc2.Int64ID(1), Result: json.RawMessage(tt.result)})
			require.NoError(t, err)
			rr := httptest.NewRecorder()
			rr.Header().Set("Content-Type", "text/event-stream")
			writer := NewResponseFilteringWriter(rr, tt.authorizer, skillRequest(t, "skills/list", `{}`), "skills/list", nil, nil)
			_, err = writer.Write(append(append([]byte("data: "), payload...), []byte("\n\n")...))
			require.NoError(t, err)
			require.NoError(t, writer.FlushAndFilter())

			if tt.contains != "" {
				assert.Contains(t, rr.Body.String(), tt.contains)
			}
			assert.NotContains(t, rr.Body.String(), tt.omits)
			if tt.internal {
				assert.Contains(t, rr.Body.String(), "internal error")
			}
		})
	}
}

func mustSkillAuthorizer(t *testing.T, policy string) authorizers.Authorizer {
	t.Helper()
	authorizer, err := cedar.NewCedarAuthorizer(cedar.ConfigOptions{Policies: []string{policy}, EntitiesJSON: `[]`}, "")
	require.NoError(t, err)
	return authorizer
}
