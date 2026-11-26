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
// the vMCP's production OIDC validator shares the server's call binding.
const chainShortLifespan = "2s"

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
// but its cancellation for the open call is accepted. Once the call has ended,
// the token gets the validator's own 401.
//
// Whether the vMCP then delivers the cancellation to the backend is not
// asserted here: that delivery is intermittent before exp too, so it is not
// part of what admission after exp decides. The server's
// TestCallBinding_AfterExpiryCancellationStopsCall checks that the
// cancellation reaches the call's handler.
func TestCallBindingIDJAG_CancelAfterExpiry(t *testing.T) {
	t.Parallel()
	c := startIDJAGChainWith(t, chainOptions{accessTokenLifespan: chainShortLifespan, longCalls: true})
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
	case <-time.After(5 * time.Second):
		t.Log("the backend did not see the cancellation; the call ends at its own limit")
	}
	result := chainAwait[[2]string](t, done, "the call did not return")
	t.Logf("the call returned %s", result[0])

	require.Eventually(t, func() bool {
		status, _, _ := c.post(t, token, session, cancel, nil)
		return status == http.StatusUnauthorized
	}, 10*time.Second, 50*time.Millisecond, "once the call has ended, its messages are refused")
	_, body, _ = c.post(t, token, session, cancel, nil)
	assert.Contains(t, body, "token is expired", "the validator's own 401: %s", body)
}

// TestCallBindingIDJAG_ElicitationAfterExpiry runs a tool call whose backend
// asks the client a question after the access token has expired: the client
// opens the session's stream after exp, receives the question there, and its
// reply is accepted and completes the call.
func TestCallBindingIDJAG_ElicitationAfterExpiry(t *testing.T) {
	t.Parallel()
	c := startIDJAGChainWith(t, chainOptions{accessTokenLifespan: chainShortLifespan, longCalls: true})
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

	reply := `{"jsonrpc":"2.0","id":` + string(question.ID) + `,"result":{"action":"accept","content":{}}}`
	status, body, _ := c.post(t, token, session, reply, nil)
	require.Equal(t, http.StatusAccepted, status, "the elicitation reply after exp: %s", body)

	result := chainAwait(t, done, "the call did not return")
	assert.Equal(t, http.StatusText(http.StatusOK), result[0], "body: %s", result[1])
	assert.Contains(t, result[1], "client said accept")
}
