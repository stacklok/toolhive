// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/vmcp/callbinding"
	"github.com/stacklok/toolhive/pkg/vmcp/core"
)

// cbExpiryServer is a call-binding test server whose token middleware stands
// in for a validator given the binding's TokenValidatorOption, with a blocking
// core, a settable clock, and a maximum call duration of three hours, longer
// than the hour test tokens live. The test issuer's tokens for cbClient may
// send their call's messages for three hours after exp.
func cbExpiryServer(t *testing.T) (*cbTestServer, *blockingCore, *cbClock) {
	t.Helper()
	return cbExpiryServerWith(t, cbAdmission(cbIssuer, cbClient, 3*time.Hour))
}

// cbAdmission is an ExpiredTokenAdmission list of one entry.
func cbAdmission(issuer, clientID string, grace time.Duration) []callbinding.ExpiredTokenAdmission {
	return []callbinding.ExpiredTokenAdmission{{Issuer: issuer, ClientID: clientID, Grace: grace}}
}

// cbExpiryServerWith is cbExpiryServer with the given ExpiredTokenAdmission
// list; nil keeps exp strict.
func cbExpiryServerWith(
	t *testing.T, admission []callbinding.ExpiredTokenAdmission,
) (*cbTestServer, *blockingCore, *cbClock) {
	t.Helper()
	bc := newBlockingCore()
	clock := &cbClock{now: time.Now()}
	s := newCallBindingTestServer(t, cbServerOptions{
		binding: callbinding.Config{MaxCallDuration: 3 * time.Hour, ExpiredTokenAdmission: admission},
		wrap: func(fc *fakeCore) core.VMCP {
			bc.fakeCore = fc
			return bc
		},
		clock:          clock,
		validateExpiry: true,
	})
	t.Cleanup(bc.unblock)
	return s, bc, clock
}

// tokenExpiry is the exp of the test token's claims.
func tokenExpiry(t *testing.T, claims map[string]any) time.Time {
	t.Helper()
	exp, ok := claims["exp"].(float64)
	require.True(t, ok, "claims carry no exp")
	return time.Unix(int64(exp), 0)
}

// requireCallMessagesRefused checks that none of an open call's messages on
// its session gets past the checks after exp: the stand-in validator refuses
// them, and so does the call-binding check behind a validator that admits any
// expired token.
func requireCallMessagesRefused(t *testing.T, s *cbTestServer, token, session string) {
	t.Helper()
	send := func() []cbResponse {
		return []cbResponse{
			s.send(t, http.MethodPost, token, session, cancelBody("7"), nil),
			s.send(t, http.MethodPost, token, session, elicitationReply, nil),
			s.send(t, http.MethodGet, token, session, "", nil),
			s.send(t, http.MethodDelete, token, session, "", nil),
		}
	}
	s.tokens.setAdmitAll(false)
	for _, resp := range send() {
		requireValidatorRefused(t, resp)
	}
	s.tokens.setAdmitAll(true)
	defer s.tokens.setAdmitAll(false)
	for _, resp := range send() {
		requireRefused(t, resp, callbinding.ReasonTokenExpired, nil)
	}
}

// TestCallBinding_AfterExpiryStrictWithoutList checks that without an
// ExpiredTokenAdmission list a call-bound token gets 401 on every request
// once it expires, even its open call's cancellation, replies, GET and
// DELETE.
func TestCallBinding_AfterExpiryStrictWithoutList(t *testing.T) {
	t.Parallel()
	s, bc, clock := cbExpiryServerWith(t, nil)
	token := s.tokens.add("bound", claimsFor(cbClient, "jti-1", []any{callEntry(cbServer, cbTool, argsHash(t, `{}`))}))
	session := s.session(t, token)

	done := s.postAsync(t.Context(), token, session, toolCallBody(7, cbTool, "{}"))
	bc.waitStarted(t)
	clock.Advance(time.Hour + time.Second)
	requireCallMessagesRefused(t, s, token, session)

	bc.unblock()
	awaitStatus(t, done)
}

// TestCallBinding_AfterExpiryGrace checks that a matching entry admits the
// call's messages only for its grace after exp, while the call is still open
// and well inside the maximum call duration.
func TestCallBinding_AfterExpiryGrace(t *testing.T) {
	t.Parallel()
	const grace = 10 * time.Minute
	s, bc, clock := cbExpiryServerWith(t, cbAdmission(cbIssuer, cbClient, grace))
	claims := claimsFor(cbClient, "jti-1", []any{callEntry(cbServer, cbTool, argsHash(t, `{}`))})
	token := s.tokens.add("bound", claims)
	session := s.session(t, token)
	exp := tokenExpiry(t, claims)

	done := s.postAsync(t.Context(), token, session, toolCallBody(7, cbTool, "{}"))
	bc.waitStarted(t)

	clock.Set(exp.Add(grace - time.Second))
	passedChecks(t, s.send(t, http.MethodPost, token, session, elicitationReply, nil))
	passedChecks(t, s.send(t, http.MethodGet, token, session, "", nil))

	clock.Set(exp.Add(grace))
	requireCallMessagesRefused(t, s, token, session)

	bc.unblock()
	awaitStatus(t, done)
}

// TestCallBinding_AfterExpiryNotListed checks that an entry matches only the
// token's iss and client_id: another issuer, another client, or a client named
// only in azp gets 401 after exp.
func TestCallBinding_AfterExpiryNotListed(t *testing.T) {
	t.Parallel()
	azpOnly := func(claims map[string]any) map[string]any {
		claims["azp"] = claims["client_id"]
		delete(claims, "client_id")
		return claims
	}
	for _, tc := range []struct {
		name      string
		admission []callbinding.ExpiredTokenAdmission
		claims    func(map[string]any) map[string]any
	}{
		{name: "another issuer", admission: cbAdmission("https://other-as.example.com", cbClient, time.Hour)},
		{name: "another client", admission: cbAdmission(cbIssuer, "other-client", time.Hour)},
		{name: "client only in azp", admission: cbAdmission(cbIssuer, cbClient, time.Hour), claims: azpOnly},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s, bc, clock := cbExpiryServerWith(t, tc.admission)
			claims := claimsFor(cbClient, "jti-1", []any{callEntry(cbServer, cbTool, argsHash(t, `{}`))})
			if tc.claims != nil {
				claims = tc.claims(claims)
			}
			token := s.tokens.add("bound", claims)
			session := s.session(t, token)

			done := s.postAsync(t.Context(), token, session, toolCallBody(7, cbTool, "{}"))
			bc.waitStarted(t)
			clock.Advance(time.Hour + time.Second)
			requireCallMessagesRefused(t, s, token, session)

			bc.unblock()
			awaitStatus(t, done)
		})
	}
}

// requireValidatorRefused checks that the stand-in validator refused the
// request, as a validator refuses an expired token the binding does not admit.
func requireValidatorRefused(t *testing.T, resp cbResponse) {
	t.Helper()
	require.Equal(t, http.StatusUnauthorized, resp.status, "body: %s", resp.body)
	assert.Contains(t, resp.body, "unauthorized")
}

// passedChecks reports whether a request passed the call-binding checks: no
// 401 or 403 from them.
func passedChecks(t *testing.T, resp cbResponse) {
	t.Helper()
	assert.NotEqual(t, http.StatusUnauthorized, resp.status, "body: %s", resp.body)
	assert.NotEqual(t, http.StatusForbidden, resp.status, "body: %s", resp.body)
}

// TestCallBinding_AfterExpiry runs a tools/call that outlives its token: after
// exp, only the open call's own messages on its session are accepted, a
// cancellation among them, until the call ends.
func TestCallBinding_AfterExpiry(t *testing.T) {
	t.Parallel()
	s, bc, clock := cbExpiryServer(t)
	token := s.tokens.add("bound", claimsFor(cbClient, "jti-1", []any{callEntry(cbServer, cbTool, argsHash(t, `{}`))}))
	session := s.session(t, token)
	otherSession := s.session(t, token)

	done := s.postAsync(t.Context(), token, session, toolCallBody(7, cbTool, "{}"))
	bc.waitStarted(t)
	clock.Advance(time.Hour + time.Second)

	// The validator refuses anything off the call's session, or that is not a
	// POST, GET or DELETE.
	requireValidatorRefused(t, s.send(t, http.MethodPost, token, otherSession, cancelBody("7"), nil))
	requireValidatorRefused(t, s.send(t, http.MethodPost, token, "", cancelBody("7"), nil))
	requireValidatorRefused(t, s.send(t, http.MethodPost, token, otherSession, elicitationReply, nil))
	requireValidatorRefused(t, s.send(t, http.MethodGet, token, otherSession, "", nil))
	requireValidatorRefused(t, s.send(t, http.MethodDelete, token, otherSession, "", nil))
	requireValidatorRefused(t, s.send(t, http.MethodPut, token, session, "", nil))

	// On the call's session, anything but the call's own messages is refused
	// with 401 token_expired: no new call, list, handshake or ping.
	for _, body := range []string{
		toolCallBody(8, cbTool, "{}"),
		`{"jsonrpc":"2.0","id":9,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":10,"method":"prompts/list"}`,
		`{"jsonrpc":"2.0","id":11,"method":"ping"}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":12,"method":"initialize","params":{"protocolVersion":"2025-11-25",` +
			`"capabilities":{},"clientInfo":{"name":"cb-test","version":"1.0"}}}`,
		cancelBody("8"),
		cancelBody(`"7"`),
		`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{}}`,
	} {
		requireRefused(t, s.send(t, http.MethodPost, token, session, body, nil), callbinding.ReasonTokenExpired, nil)
	}
	assert.Zero(t, s.fc.callToolCalls.Load(), "no call starts after exp")

	// The call's own messages pass: a response (such as an elicitation
	// reply), a GET on its session, and the cancellation, which goes last as
	// it ends the call.
	passedChecks(t, s.send(t, http.MethodPost, token, session, elicitationReply, nil))
	get := s.send(t, http.MethodGet, token, session, "", nil)
	passedChecks(t, get)
	cancel := s.send(t, http.MethodPost, token, session, cancelBody("7"), nil)
	assert.Equal(t, http.StatusAccepted, cancel.status, "body: %s", cancel.body)
	awaitStatus(t, done)

	// Once the call has ended, nothing is accepted.
	key := cbIssuer + "\x00jti-1"
	require.Eventually(t, func() bool {
		call, found, err := s.srv.callBinding.binding.Record().Lookup(context.Background(), key)
		return err == nil && found && call.Ended
	}, 10*time.Second, 10*time.Millisecond)
	requireValidatorRefused(t, s.send(t, http.MethodPost, token, session, cancelBody("7"), nil))
	requireValidatorRefused(t, s.send(t, http.MethodPost, token, session, elicitationReply, nil))
	requireValidatorRefused(t, s.send(t, http.MethodGet, token, session, "", nil))
	requireValidatorRefused(t, s.send(t, http.MethodDelete, token, session, "", nil))
}

// TestCallBinding_AfterExpiryCancellationStopsCall checks that a cancellation
// sent after exp reaches the call: the call's handler sees its context
// cancelled and the call never completes.
func TestCallBinding_AfterExpiryCancellationStopsCall(t *testing.T) {
	t.Parallel()
	s, bc, clock := cbExpiryServer(t)
	token := s.tokens.add("bound", claimsFor(cbClient, "jti-1", []any{callEntry(cbServer, cbTool, argsHash(t, `{}`))}))
	session := s.session(t, token)

	done := s.postAsync(t.Context(), token, session, toolCallBody(7, cbTool, "{}"))
	bc.waitStarted(t)
	clock.Advance(time.Hour + time.Second)

	cancel := s.send(t, http.MethodPost, token, session, cancelBody("7"), nil)
	require.Equal(t, http.StatusAccepted, cancel.status, "body: %s", cancel.body)
	awaitStatus(t, done)
	assert.Zero(t, s.fc.callToolCalls.Load(), "the cancelled call must not complete")
}

// TestCallBinding_AfterExpiryCancelAfterDroppedPost checks that a Legacy
// client that closes the call's POST and then cancels after exp, as a go-sdk
// client does, still reaches the call: the call stays open while its dispatch
// runs.
func TestCallBinding_AfterExpiryCancelAfterDroppedPost(t *testing.T) {
	t.Parallel()
	s, bc, clock := cbExpiryServer(t)
	token := s.tokens.add("bound", claimsFor(cbClient, "jti-1", []any{callEntry(cbServer, cbTool, argsHash(t, `{}`))}))
	session := s.session(t, token)

	ctx, cancelPost := context.WithCancel(t.Context())
	done := s.postAsync(ctx, token, session, toolCallBody(7, cbTool, "{}"))
	bc.waitStarted(t)
	cancelPost()
	awaitStatus(t, done)
	clock.Advance(time.Hour + time.Second)

	cancel := s.send(t, http.MethodPost, token, session, cancelBody("7"), nil)
	require.Equal(t, http.StatusAccepted, cancel.status, "body: %s", cancel.body)
	requireCallEnded(t, s.srv.callBinding.binding.Record(), true)
	assert.Zero(t, s.fc.callToolCalls.Load(), "the cancelled call must not complete")
	requireValidatorRefused(t, s.send(t, http.MethodPost, token, session, cancelBody("7"), nil))
}

// TestCallBinding_AfterExpiryDelete checks that a DELETE on the open call's
// session is accepted after exp. Deleting a session cancels its open calls, so
// the DELETE ends the session and the call, which never completes.
func TestCallBinding_AfterExpiryDelete(t *testing.T) {
	t.Parallel()
	s, bc, clock := cbExpiryServer(t)
	token := s.tokens.add("bound", claimsFor(cbClient, "jti-1", []any{callEntry(cbServer, cbTool, argsHash(t, `{}`))}))
	session := s.session(t, token)

	done := s.postAsync(t.Context(), token, session, toolCallBody(7, cbTool, "{}"))
	bc.waitStarted(t)
	clock.Advance(time.Hour + time.Second)

	deleted := s.send(t, http.MethodDelete, token, session, "", nil)
	assert.GreaterOrEqual(t, deleted.status, http.StatusOK, "body: %s", deleted.body)
	assert.Less(t, deleted.status, http.StatusMultipleChoices,
		"the DELETE passes the checks and ends the session: %s", deleted.body)
	awaitStatus(t, done)
	assert.Zero(t, s.fc.callToolCalls.Load(), "deleting the session cancels the call")
}

// TestCallBinding_AfterExpiryMaxCallDuration checks that after exp the call's
// messages are accepted only until the call has run for the maximum call
// duration, even while its request is still open.
func TestCallBinding_AfterExpiryMaxCallDuration(t *testing.T) {
	t.Parallel()
	s, bc, clock := cbExpiryServer(t)
	token := s.tokens.add("bound", claimsFor(cbClient, "jti-1", []any{callEntry(cbServer, cbTool, argsHash(t, `{}`))}))
	session := s.session(t, token)

	done := s.postAsync(t.Context(), token, session, toolCallBody(7, cbTool, "{}"))
	bc.waitStarted(t)

	clock.Advance(3*time.Hour - time.Second)
	passedChecks(t, s.send(t, http.MethodPost, token, session, elicitationReply, nil))
	clock.Advance(time.Second)
	requireValidatorRefused(t, s.send(t, http.MethodPost, token, session, elicitationReply, nil))
	requireValidatorRefused(t, s.send(t, http.MethodPost, token, session, cancelBody("7"), nil))
	requireValidatorRefused(t, s.send(t, http.MethodGet, token, session, "", nil))

	bc.unblock()
	awaitStatus(t, done)
}

// TestCallBinding_AfterExpiryListCall checks that a list call, which is
// recorded ended, has no messages after exp: not even a GET or DELETE on its
// session, and no response.
func TestCallBinding_AfterExpiryListCall(t *testing.T) {
	t.Parallel()
	s, _, clock := cbExpiryServer(t)
	token := s.tokens.add("list", claimsFor(cbClient, "jti-1", []any{listEntry(cbServer, "tools/list")}))
	session := s.session(t, token)
	list := s.send(t, http.MethodPost, token, session, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`, nil)
	require.Equal(t, http.StatusOK, list.status, "body: %s", list.body)

	clock.Advance(time.Hour + time.Second)
	requireValidatorRefused(t, s.send(t, http.MethodGet, token, session, "", nil))
	requireValidatorRefused(t, s.send(t, http.MethodDelete, token, session, "", nil))
	requireValidatorRefused(t, s.send(t, http.MethodPost, token, session, elicitationReply, nil))
	requireValidatorRefused(t, s.send(t, http.MethodPost, token, session, cancelBody("2"), nil))
}

// TestCallBinding_AfterExpiryModernCall checks that a sessionless (Modern)
// call that outlives its token has no messages after exp: its client cancels
// by closing the request, which exp does not touch.
func TestCallBinding_AfterExpiryModernCall(t *testing.T) {
	t.Parallel()
	s, bc, clock := cbExpiryServer(t)
	token := s.tokens.add("bound", claimsFor(cbClient, "jti-1", []any{callEntry(cbServer, cbTool, argsHash(t, `{}`))}))

	done := s.sendAsync(t.Context(), http.MethodPost, token, "", cbModernCall(7, `{}`), cbModernHeaders())
	bc.waitStarted(t)
	clock.Advance(time.Hour + time.Second)

	requireValidatorRefused(t, s.send(t, http.MethodPost, token, "", cancelBody("7"), nil))
	requireValidatorRefused(t, s.send(t, http.MethodPost, token, "", elicitationReply, nil))
	requireValidatorRefused(t, s.send(t, http.MethodPost, token, "", cbModernCall(8, `{}`), cbModernHeaders()))

	// The open request still completes after exp.
	bc.unblock()
	assert.Equal(t, http.StatusOK, awaitStatus(t, done))
}

// TestCallBinding_AdmittedIdentityNeverStartsACall checks the checks behind
// the validator: even when a validator accepts any expired token, an identity
// marked as admitted after exp cannot start a call or open a session, and a
// marked identity whose token carries no entry is refused outright.
func TestCallBinding_AdmittedIdentityNeverStartsACall(t *testing.T) {
	t.Parallel()
	s, _, clock := cbExpiryServer(t)
	s.tokens.setAdmitAll(true)
	bound := s.tokens.add("bound", claimsFor(cbClient, "jti-1", []any{callEntry(cbServer, cbTool, argsHash(t, `{}`))}))
	plain := s.tokens.add("plain", claimsFor("other-client", "jti-2", nil))
	boundSession := s.session(t, bound)
	plainSession := s.session(t, plain)
	clock.Advance(time.Hour + time.Second)

	requireRefused(t, s.send(t, http.MethodPost, bound, boundSession, toolCallBody(2, cbTool, "{}"), nil),
		callbinding.ReasonTokenExpired, nil)
	requireRefused(t, s.initialize(t, bound), callbinding.ReasonTokenExpired, nil)
	requireRefused(t, s.send(t, http.MethodGet, bound, boundSession, "", nil), callbinding.ReasonTokenExpired, nil)
	requireRefused(t, s.send(t, http.MethodPost, plain, plainSession, toolCallBody(3, cbTool, "{}"), nil),
		callbinding.ReasonTokenExpired, nil)
	requireRefused(t, s.send(t, http.MethodGet, plain, plainSession, "", nil), callbinding.ReasonTokenExpired, nil)
	assert.Zero(t, s.fc.callToolCalls.Load())
}

// TestCallBinding_UnwiredValidatorWarnsOnce checks that the vMCP warns once
// when call-bound tool calls start, some tokens may send messages after exp,
// and the token validator was never given the binding's option. It stays
// silent once the validator was, and without an ExpiredTokenAdmission list,
// as exp is then strict whether or not the validator was given it.
// Not parallel: it swaps the process-global slog default.
//
//nolint:paralleltest // slog.SetDefault mutates process-global state
func TestCallBinding_UnwiredValidatorWarnsOnce(t *testing.T) {
	var buf logSyncBuffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	const warning = "the token validator was not given the call binding's TokenValidatorOption"

	runTwoCalls := func(t *testing.T, wire bool, admission []callbinding.ExpiredTokenAdmission) {
		t.Helper()
		s := newCallBindingTestServer(t, cbServerOptions{
			binding: callbinding.Config{ExpiredTokenAdmission: admission},
		})
		if wire {
			_ = s.srv.callBinding.binding.TokenValidatorOption()
		}
		for _, jti := range []string{"jti-1", "jti-2"} {
			token := s.tokens.add(jti, claimsFor(cbClient, jti, []any{callEntry(cbServer, cbTool, argsHash(t, `{}`))}))
			requireCallRan(t, s.send(t, http.MethodPost, token, s.session(t, token), toolCallBody(2, cbTool, "{}"), nil))
		}
	}

	listed := cbAdmission(cbIssuer, cbClient, time.Minute)
	runTwoCalls(t, false, nil)
	assert.NotContains(t, buf.String(), warning, "without a list, exp is strict and there is nothing to warn about")
	runTwoCalls(t, true, listed)
	assert.NotContains(t, buf.String(), warning, "a wired validator gives no warning")
	runTwoCalls(t, false, listed)
	assert.Equal(t, 1, strings.Count(buf.String(), warning), "logged once: %s", buf.String())
}
