// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokenexchange_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ory/fosite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/authserver/server/session"
	"github.com/stacklok/toolhive/pkg/authserver/server/tokenexchange"
	"github.com/stacklok/toolhive/pkg/authserver/server/tokenexchange/mocks"
)

const (
	callTestToken   = "eyJ.secret-actor-token.sig"
	callTestTimeout = time.Millisecond
	callTestBound   = tokenexchange.ActorValidationTimeoutForTest
)

var callTestNow = time.Unix(1_800_000_000, 0)

func callTestRequest() tokenexchange.ActorRequest {
	return tokenexchange.ActorRequest{
		ActorToken:     callTestToken,
		ActorTokenType: "urn:ietf:params:oauth:token-type:jwt",
		ClientID:       "client-1",
		Grant: tokenexchange.GrantOwner{
			Subject:           "user-1",
			SubjectIdentifier: `{"format":"iss_sub"}`,
			UpstreamSubject: session.UpstreamSubject{
				Issuer: "https://idp", Subject: "up-sub-1", StableClaim: "uid", StableValue: "u1",
			},
		},
		Target: tokenexchange.ActorTarget{Issuer: "https://gw", Resource: "https://gw/mcp"},
	}
}

func callTestOK() tokenexchange.ActorResult {
	return tokenexchange.ActorResult{AgentID: "agent-1", ExpiresAt: callTestNow.Add(time.Hour)}
}

func fixedClock() time.Time { return callTestNow }

// serverCause asserts err is a server_error whose client description is the
// generic hint only, and returns the cause text behind it.
func serverCause(t *testing.T, err error) string {
	t.Helper()
	var rfc *fosite.RFC6749Error
	require.ErrorAs(t, err, &rfc)
	assert.Equal(t, "server_error", rfc.ErrorField)
	assert.Equal(t, http.StatusInternalServerError, rfc.CodeField)
	desc := rfc.GetDescription()
	assert.NotContains(t, desc, callTestToken)
	assert.NotContains(t, desc, "[redacted]")
	cause := errors.Unwrap(rfc)
	require.NotNil(t, cause)
	return cause.Error()
}

func waitClosed(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(callTestBound):
		t.Fatal("timed out waiting for the validator goroutine")
	}
}

// TestValidateActorSuccessDoesNotCache calls twice and expects the validator to
// run both times, because validateActor caches nothing.
func TestValidateActorSuccessDoesNotCache(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	v := mocks.NewMockActorTokenValidator(ctrl)
	req := callTestRequest()
	v.EXPECT().ValidateActor(gomock.Any(), req).Return(callTestOK(), nil).Times(2)

	for range 2 {
		res, checkedAt, reason, err := tokenexchange.ValidateActorForTest(context.Background(), v, req, fixedClock, callTestBound)
		require.NoError(t, err)
		assert.Equal(t, callTestOK(), res)
		assert.Equal(t, callTestNow, checkedAt)
		assert.Empty(t, reason)
	}
}

func TestValidateActorTimeoutHonoringContext(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	v := mocks.NewMockActorTokenValidator(ctrl)
	v.EXPECT().ValidateActor(gomock.Any(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, _ tokenexchange.ActorRequest) (tokenexchange.ActorResult, error) {
			<-ctx.Done()
			return tokenexchange.ActorResult{}, ctx.Err()
		}).MaxTimes(100)

	// Loop because the validator's error and ctx.Done() become ready together, and
	// select picks either at random. The context state must decide every time.
	for i := range 100 {
		res, _, reason, err := tokenexchange.ValidateActorForTest(
			context.Background(), v, callTestRequest(), fixedClock, callTestTimeout)
		require.Error(t, err, "run %d", i)
		require.Equal(t, "actor_validator_timeout", reason, "run %d", i)
		assert.Equal(t, tokenexchange.ActorResult{}, res)
		assert.Equal(t, fmt.Sprintf("validator did not return within %s", callTestTimeout), serverCause(t, err))
	}
}

func TestValidateActorTimeoutIgnoringContext(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	v := mocks.NewMockActorTokenValidator(ctrl)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseMock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseMock)
	done := make(chan struct{})
	v.EXPECT().ValidateActor(gomock.Any(), gomock.Any()).DoAndReturn(
		func(context.Context, tokenexchange.ActorRequest) (tokenexchange.ActorResult, error) {
			defer close(done)
			<-release
			return callTestOK(), nil
		})

	start := time.Now()
	_, _, reason, err := tokenexchange.ValidateActorForTest(
		context.Background(), v, callTestRequest(), fixedClock, callTestTimeout)
	require.Error(t, err)
	assert.Equal(t, "actor_validator_timeout", reason)
	// The call returned without waiting for the validator, which is still blocked.
	assert.Less(t, time.Since(start), callTestBound)

	// done proves the validator returned. The goroutine's following send
	// cannot block because the outcome channel has capacity 1.
	releaseMock()
	waitClosed(t, done)
}

const canceledCause = "request canceled while validating the actor"

func TestValidateActorCanceledBeforeCall(t *testing.T) {
	t.Parallel()
	// No expectation is set, so any call to the validator fails the test.
	v := mocks.NewMockActorTokenValidator(gomock.NewController(t))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, _, reason, err := tokenexchange.ValidateActorForTest(ctx, v, callTestRequest(), fixedClock, callTestBound)
	require.Error(t, err)
	assert.Equal(t, "actor_validator_canceled", reason)
	assert.Equal(t, canceledCause, serverCause(t, err))
}

func TestValidateActorCanceledDuringCall(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// parent returns the parent context and the function the validator calls to end it.
		parent func(t *testing.T) (context.Context, func())
	}{
		{
			name: "parent canceled",
			parent: func(t *testing.T) (context.Context, func()) {
				t.Helper()
				ctx, cancel := context.WithCancel(context.Background())
				t.Cleanup(cancel)
				return ctx, cancel
			},
		},
		{
			name: "parent deadline expires before the budget (reported as canceled)",
			parent: func(t *testing.T) (context.Context, func()) {
				t.Helper()
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
				t.Cleanup(cancel)
				return ctx, func() {}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, end := tt.parent(t)
			v := mocks.NewMockActorTokenValidator(gomock.NewController(t))
			// MaxTimes, not Times: a stalled scheduler can let the 5ms deadline pass
			// before the goroutine starts, which is still a cancellation.
			v.EXPECT().ValidateActor(gomock.Any(), gomock.Any()).DoAndReturn(
				func(vctx context.Context, _ tokenexchange.ActorRequest) (tokenexchange.ActorResult, error) {
					end()
					<-vctx.Done()
					return tokenexchange.ActorResult{}, vctx.Err()
				}).MaxTimes(1)

			_, _, reason, err := tokenexchange.ValidateActorForTest(ctx, v, callTestRequest(), fixedClock, callTestBound)
			require.Error(t, err)
			assert.Equal(t, "actor_validator_canceled", reason)
			assert.Equal(t, canceledCause, serverCause(t, err))
		})
	}
}

func TestValidateActorValidatorFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		token      string
		validate   func(context.Context, tokenexchange.ActorRequest) (tokenexchange.ActorResult, error)
		wantReason string
		wantCause  string
		// prefixOnly compares only the start of the cause, for text owned by the Go runtime.
		prefixOnly bool
	}{
		{
			name:  "panic with a string",
			token: callTestToken,
			validate: func(context.Context, tokenexchange.ActorRequest) (tokenexchange.ActorResult, error) {
				panic("bad token " + callTestToken)
			},
			wantReason: "actor_validator_panic",
			wantCause:  "validator panicked: string: bad token [redacted]",
		},
		{
			name:  "panic with an error",
			token: callTestToken,
			validate: func(context.Context, tokenexchange.ActorRequest) (tokenexchange.ActorResult, error) {
				panic(errors.New("bad token " + callTestToken))
			},
			wantReason: "actor_validator_panic",
			wantCause:  "validator panicked: *errors.errorString: bad token [redacted]",
		},
		{
			name:  "panic with nil",
			token: callTestToken,
			validate: func(context.Context, tokenexchange.ActorRequest) (tokenexchange.ActorResult, error) {
				panic(nil)
			},
			wantReason: "actor_validator_panic",
			wantCause:  "validator panicked: *runtime.PanicNilError: ",
			prefixOnly: true,
		},
		{
			name:  "panic value whose text panics twice",
			token: callTestToken,
			validate: func(context.Context, tokenexchange.ActorRequest) (tokenexchange.ActorResult, error) {
				panic(panicsInError{next: panicsInError{next: panicsInError{}}})
			},
			wantReason: "actor_validator_panic",
			wantCause:  "validator panicked with an unprintable value",
		},
		{
			name:  "returned error whose text panics",
			token: callTestToken,
			validate: func(context.Context, tokenexchange.ActorRequest) (tokenexchange.ActorResult, error) {
				return tokenexchange.ActorResult{}, panicsInError{}
			},
			wantReason: "actor_validator_panic",
			wantCause:  "validator panicked: string: error text panics",
		},
		{
			name:  "returned error whose As method panics",
			token: callTestToken,
			validate: func(context.Context, tokenexchange.ActorRequest) (tokenexchange.ActorResult, error) {
				return tokenexchange.ActorResult{}, panicsInAs{}
			},
			wantReason: "actor_validator_panic",
			wantCause:  "validator panicked: string: as panics",
		},
		{
			name:  "returned error",
			token: callTestToken,
			validate: func(context.Context, tokenexchange.ActorRequest) (tokenexchange.ActorResult, error) {
				return tokenexchange.ActorResult{}, errors.New("lookup " + callTestToken + " failed for " + callTestToken)
			},
			wantReason: "actor_validator_error",
			wantCause:  "lookup [redacted] failed for [redacted]",
		},
		{
			name:  "returned error is the validator's own deadline",
			token: callTestToken,
			validate: func(context.Context, tokenexchange.ActorRequest) (tokenexchange.ActorResult, error) {
				return tokenexchange.ActorResult{}, context.DeadlineExceeded
			},
			wantReason: "actor_validator_error",
			wantCause:  "context deadline exceeded",
		},
		{
			name:  "empty token leaves the cause unchanged",
			token: "",
			validate: func(context.Context, tokenexchange.ActorRequest) (tokenexchange.ActorResult, error) {
				return tokenexchange.ActorResult{}, errors.New("boom")
			},
			wantReason: "actor_validator_error",
			wantCause:  "boom",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctrl := gomock.NewController(t)
			v := mocks.NewMockActorTokenValidator(ctrl)
			v.EXPECT().ValidateActor(gomock.Any(), gomock.Any()).DoAndReturn(tt.validate)
			req := callTestRequest()
			req.ActorToken = tt.token

			res, _, reason, err := tokenexchange.ValidateActorForTest(context.Background(), v, req, fixedClock, callTestBound)
			require.Error(t, err)
			assert.Equal(t, tokenexchange.ActorResult{}, res)
			assert.Equal(t, tt.wantReason, reason)
			cause := serverCause(t, err)
			if tt.prefixOnly {
				assert.True(t, strings.HasPrefix(cause, tt.wantCause), cause)
			} else {
				assert.Equal(t, tt.wantCause, cause)
			}
			if tt.token != "" {
				assert.NotContains(t, err.Error(), tt.token)
			}
		})
	}
}

// panicsInError panics from Error. With next set, the panic value is itself a
// panicsInError, so printing it panics again.
type panicsInError struct{ next error }

func (e panicsInError) Error() string {
	if e.next != nil {
		panic(e.next)
	}
	panic("error text panics")
}

type panicsInAs struct{}

func (panicsInAs) Error() string { return "as" }
func (panicsInAs) As(any) bool   { panic("as panics") }

func TestValidateActorCheckedAtReadAfterCall(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	v := mocks.NewMockActorTokenValidator(ctrl)
	// The validator goroutine writes now before it sends its outcome, and the
	// caller reads the clock after it receives it, so no lock is needed.
	now := callTestNow
	advanced := callTestNow.Add(10 * time.Second)
	v.EXPECT().ValidateActor(gomock.Any(), gomock.Any()).DoAndReturn(
		func(context.Context, tokenexchange.ActorRequest) (tokenexchange.ActorResult, error) {
			now = advanced
			return tokenexchange.ActorResult{AgentID: "agent-1", ExpiresAt: callTestNow.Add(5 * time.Second)}, nil
		})

	res, checkedAt, reason, err := tokenexchange.ValidateActorForTest(
		context.Background(), v, callTestRequest(), func() time.Time { return now }, callTestBound)
	require.Error(t, err)
	assert.Equal(t, advanced, checkedAt)
	assert.Equal(t, "actor_token_expired", reason)
	assert.Equal(t, tokenexchange.ActorResult{}, res)
}
