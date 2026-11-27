// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server_test

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The tests in this file run a call that outlives its access token through
// the real chain: AS-2 issues access tokens that live chainShortLifespan, and
// the vMCP's production OIDC validator shares the server's call binding. Unless
// a test says otherwise, the vMCP lets AS-2's tokens for chainAS2Client send
// their call's messages for chainGrace after exp.
const (
	chainShortLifespan = "2s"
	chainGrace         = time.Minute
)

// waitPastExp sleeps until token's exp has passed by a second.
func waitPastExp(t *testing.T, token string) {
	t.Helper()
	var exp int64
	require.NoError(t, json.Unmarshal(chainJWTClaim(t, token, "exp"), &exp))
	time.Sleep(time.Until(time.Unix(exp, 0).Add(time.Second)))
}

// chainAsyncPost sends body to the vMCP off the test goroutine, and reports
// its status and body. It must not use require.
func (c *idJAGChain) chainAsyncPost(ctx context.Context, token, session, body string) <-chan [2]string {
	done := make(chan [2]string, 1)
	go func() {
		out := [2]string{"0", ""}
		defer func() { done <- out }()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.vmcpURL+"/mcp", strings.NewReader(body))
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Mcp-Session-Id", session)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			out[1] = err.Error()
			return
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		out = [2]string{http.StatusText(resp.StatusCode), string(raw)}
	}()
	return done
}

// chainAwait waits for a signal from the backend.
func chainAwait[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(30 * time.Second):
		require.FailNow(t, what)
		var zero T
		return zero
	}
}

// chainElicitingSession opens a Legacy session whose client declares the
// elicitation capability.
func (c *idJAGChain) chainElicitingSession(t *testing.T, token string) string {
	t.Helper()
	status, body, session := c.post(t, token, "", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":`+
		`{"protocolVersion":"2025-11-25","capabilities":{"elicitation":{}},"clientInfo":{"name":"cb","version":"1"}}}`, nil)
	require.Equal(t, http.StatusOK, status, "initialize: %s", body)
	status, body, _ = c.post(t, token, session, `{"jsonrpc":"2.0","method":"notifications/initialized"}`, nil)
	require.Equal(t, http.StatusAccepted, status, "notifications/initialized: %s", body)
	return session
}

// TestCallBindingIDJAG_CancelAfterExpiry runs a tool call that outlives its
// access token: after exp the token cannot start another call or list tools,
// but its cancellation for the open call is accepted and reaches the backend
// before the call's own deadline. Once the call has ended, within the grace, a
// repeated cancellation is ignored with 202 and a GET gets 403 no_open_call.
func TestCallBindingIDJAG_CancelAfterExpiry(t *testing.T) {
	t.Parallel()
	c := startIDJAGChainWith(t, chainOptions{
		accessTokenLifespan: chainShortLifespan, longCalls: true, expiredTokenGrace: chainGrace,
	})
	token := c.accessToken(t, chainToolEntry(t, "alpha_wait", `{}`))
	session := c.session(t, token)

	done := c.chainAsyncPost(t.Context(), token, session, cbToolCall(7, "alpha_wait", `{}`))
	chainAwait(t, c.hooks.waitStarted, "the backend call did not start")
	waitPastExp(t, token)

	status, body, _ := c.post(t, token, session, cbToolCall(8, "alpha_wait", `{}`), nil)
	assert.Equal(t, http.StatusUnauthorized, status, "no new call after exp: %s", body)
	assert.Contains(t, body, "(token_expired)")
	status, body, _ = c.post(t, token, session, `{"jsonrpc":"2.0","id":9,"method":"tools/list"}`, nil)
	assert.Equal(t, http.StatusUnauthorized, status, "no list after exp: %s", body)
	assert.Contains(t, body, "(token_expired)")

	cancel := `{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":7}}`
	status, body, _ = c.post(t, token, session, cancel, nil)
	require.Equal(t, http.StatusAccepted, status, "the cancellation after exp: %s", body)
	select {
	case <-c.hooks.waitCancelled:
	case <-time.After(chainWaitLimit):
		require.FailNow(t, "the backend did not see the cancellation before the call's own deadline")
	}
	result := chainAwait[[2]string](t, done, "the call did not return")
	t.Logf("the call returned %s", result[0])

	// Once the call has ended, within the grace, nothing reaches it, and the
	// answers keep a go-sdk client's connection: 202 for a cancellation, 403
	// no_open_call with a JSON-RPC error for a GET.
	require.Eventually(t, func() bool {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, c.vmcpURL+"/mcp", nil)
		require.NoError(t, err)
		req.Header.Set("Accept", "text/event-stream")
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Mcp-Session-Id", session)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		raw, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return resp.StatusCode == http.StatusForbidden && strings.Contains(string(raw), "no_open_call")
	}, 10*time.Second, 50*time.Millisecond, "once the call has ended, a GET gets 403 no_open_call")
	status, body, _ = c.post(t, token, session, cancel, nil)
	assert.Equal(t, http.StatusAccepted, status, "a cancellation of the ended call is ignored: %s", body)
}

// TestCallBindingIDJAG_ElicitationAfterExpiry runs a tool call whose backend
// asks the client a question after the access token has expired: the client
// opens the session's stream after exp, receives the question there, and its
// reply is accepted and completes the call.
func TestCallBindingIDJAG_ElicitationAfterExpiry(t *testing.T) {
	t.Parallel()
	c := startIDJAGChainWith(t, chainOptions{
		accessTokenLifespan: chainShortLifespan, longCalls: true, expiredTokenGrace: chainGrace,
	})
	token := c.accessToken(t, chainToolEntry(t, "alpha_ask", `{}`))
	session := c.chainElicitingSession(t, token)

	done := c.chainAsyncPost(t.Context(), token, session, cbToolCall(7, "alpha_ask", `{}`))
	chainAwait(t, c.hooks.askStarted, "the backend call did not start")
	waitPastExp(t, token)

	// The session's stream, opened after exp, carries the question.
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.vmcpURL+"/mcp", nil)
	require.NoError(t, err)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Mcp-Session-Id", session)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	require.Equal(t, http.StatusOK, resp.StatusCode, "the GET on the call's session after exp")

	events := make(chan string, 16)
	go func() {
		defer close(events)
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			if data, ok := strings.CutPrefix(scanner.Text(), "data: "); ok {
				events <- data
			}
		}
	}()
	close(c.hooks.askNow)

	var question struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
	}
	deadline := time.After(30 * time.Second)
	for question.Method != "elicitation/create" {
		select {
		case data, open := <-events:
			require.True(t, open, "the stream closed before the question arrived")
			require.NoError(t, json.Unmarshal([]byte(data), &question), "event: %s", data)
		case <-deadline:
			require.FailNow(t, "the question did not arrive on the stream")
		}
	}

	// Any response on the call's session is admitted, so one the server is
	// not waiting for must be harmless: go-sdk drops it, and the question
	// stays pending for its own answer.
	stray := `{"jsonrpc":"2.0","id":"not-asked","result":{"action":"decline"}}`
	status, body, _ := c.post(t, token, session, stray, nil)
	require.Equal(t, http.StatusAccepted, status, "a response nobody asked for, after exp: %s", body)
	select {
	case result := <-done:
		require.FailNow(t, "the call ended on a response nobody asked for", "%v", result)
	case <-time.After(500 * time.Millisecond):
	}

	reply := `{"jsonrpc":"2.0","id":` + string(question.ID) + `,"result":{"action":"accept","content":{}}}`
	status, body, _ = c.post(t, token, session, reply, nil)
	require.Equal(t, http.StatusAccepted, status, "the elicitation reply after exp: %s", body)

	result := chainAwait(t, done, "the call did not return")
	assert.Equal(t, http.StatusText(http.StatusOK), result[0], "body: %s", result[1])
	assert.Contains(t, result[1], "client said accept")
}

// TestCallBindingIDJAG_StrictExpiryWithoutList runs a tool call that outlives
// its access token on a vMCP with no ExpiredTokenAdmission list: the
// production validator keeps exp strict, so after exp even the open call's
// cancellation, GET and DELETE get its own 401.
func TestCallBindingIDJAG_StrictExpiryWithoutList(t *testing.T) {
	t.Parallel()
	c := startIDJAGChainWith(t, chainOptions{accessTokenLifespan: chainShortLifespan, longCalls: true})
	token := c.accessToken(t, chainToolEntry(t, "alpha_wait", `{}`))
	session := c.session(t, token)

	ctx, closeCall := context.WithCancel(t.Context())
	done := c.chainAsyncPost(ctx, token, session, cbToolCall(7, "alpha_wait", `{}`))
	chainAwait(t, c.hooks.waitStarted, "the backend call did not start")
	waitPastExp(t, token)

	cancel := `{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":7}}`
	status, body, _ := c.post(t, token, session, cancel, nil)
	assert.Equal(t, http.StatusUnauthorized, status, "the cancellation after exp: %s", body)
	assert.Contains(t, body, "token is expired", "the validator's own 401: %s", body)
	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		req, err := http.NewRequestWithContext(t.Context(), method, c.vmcpURL+"/mcp", nil)
		require.NoError(t, err)
		req.Header.Set("Accept", "text/event-stream")
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Mcp-Session-Id", session)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		_ = resp.Body.Close()
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, "%s after exp", method)
	}

	// The client can still close the call's own request.
	closeCall()
	chainAwait[[2]string](t, done, "the call did not return")
}
