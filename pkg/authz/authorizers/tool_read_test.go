// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authorizers_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/authz/authorizers"
	"github.com/stacklok/toolhive/pkg/authz/authorizers/mocks"
)

// fakeOps is the shared generated mock with a behavior: it allows exactly the
// operations in allowed, returns errs[operation] when set, and records every call.
type fakeOps struct {
	*mocks.MockAuthorizer
	calls []authorizers.MCPOperation
}

func newFakeOps(t *testing.T, allowed []authorizers.MCPOperation, errs map[authorizers.MCPOperation]error) *fakeOps {
	t.Helper()
	allow := map[authorizers.MCPOperation]bool{}
	for _, op := range allowed {
		allow[op] = true
	}
	f := &fakeOps{MockAuthorizer: mocks.NewMockAuthorizer(gomock.NewController(t))}
	f.EXPECT().AuthorizeWithJWTClaims(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, _ authorizers.MCPFeature, op authorizers.MCPOperation,
			_ string, _ map[string]interface{},
		) (bool, error) {
			f.calls = append(f.calls, op)
			if err := errs[op]; err != nil {
				return false, err
			}
			return allow[op], nil
		}).AnyTimes()
	return f
}

func TestToolCallImpliesRead(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		allowed   []authorizers.MCPOperation
		feature   authorizers.MCPFeature
		operation authorizers.MCPOperation
		want      bool
		wantCalls []authorizers.MCPOperation
	}{
		{
			name:      "tool:read allowed directly is granted without a call check",
			allowed:   []authorizers.MCPOperation{authorizers.MCPOperationRead},
			feature:   authorizers.MCPFeatureTool,
			operation: authorizers.MCPOperationRead,
			want:      true,
			wantCalls: []authorizers.MCPOperation{authorizers.MCPOperationRead},
		},
		{
			name:      "tool:call alone implies tool:read",
			allowed:   []authorizers.MCPOperation{authorizers.MCPOperationCall},
			feature:   authorizers.MCPFeatureTool,
			operation: authorizers.MCPOperationRead,
			want:      true,
			wantCalls: []authorizers.MCPOperation{authorizers.MCPOperationRead, authorizers.MCPOperationCall},
		},
		{
			name:      "neither tool:read nor tool:call denies",
			allowed:   nil,
			feature:   authorizers.MCPFeatureTool,
			operation: authorizers.MCPOperationRead,
			want:      false,
			wantCalls: []authorizers.MCPOperation{authorizers.MCPOperationRead, authorizers.MCPOperationCall},
		},
		{
			name:      "tool:read alone never implies tool:call",
			allowed:   []authorizers.MCPOperation{authorizers.MCPOperationRead},
			feature:   authorizers.MCPFeatureTool,
			operation: authorizers.MCPOperationCall,
			want:      false,
			wantCalls: []authorizers.MCPOperation{authorizers.MCPOperationCall},
		},
		{
			name:      "tool:call passes through unchanged",
			allowed:   []authorizers.MCPOperation{authorizers.MCPOperationCall},
			feature:   authorizers.MCPFeatureTool,
			operation: authorizers.MCPOperationCall,
			want:      true,
			wantCalls: []authorizers.MCPOperation{authorizers.MCPOperationCall},
		},
		{
			name:      "non-tool read is not rewritten to a tool:call check",
			allowed:   []authorizers.MCPOperation{authorizers.MCPOperationCall},
			feature:   authorizers.MCPFeatureResource,
			operation: authorizers.MCPOperationRead,
			want:      false,
			wantCalls: []authorizers.MCPOperation{authorizers.MCPOperationRead},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			inner := newFakeOps(t, tc.allowed, nil)

			a := authorizers.WithToolCallImpliesRead(inner)
			ctx := context.Background()
			got, err := a.AuthorizeWithJWTClaims(ctx, tc.feature, tc.operation, "weather", nil)

			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
			assert.Equal(t, tc.wantCalls, inner.calls)
		})
	}
}

func TestToolCallImpliesRead_ErrorPaths(t *testing.T) {
	t.Parallel()

	errRead := errors.New("PDP unavailable")
	errCall := errors.New("call check failed")

	tests := []struct {
		name      string
		allowed   []authorizers.MCPOperation
		errs      map[authorizers.MCPOperation]error
		wantErr   error
		wantCalls []authorizers.MCPOperation
	}{
		{
			name:      "read error is returned without a call check",
			allowed:   []authorizers.MCPOperation{authorizers.MCPOperationCall},
			errs:      map[authorizers.MCPOperation]error{authorizers.MCPOperationRead: errRead},
			wantErr:   errRead,
			wantCalls: []authorizers.MCPOperation{authorizers.MCPOperationRead},
		},
		{
			name:      "read timeout is returned without a call check",
			allowed:   []authorizers.MCPOperation{authorizers.MCPOperationCall},
			errs:      map[authorizers.MCPOperation]error{authorizers.MCPOperationRead: context.DeadlineExceeded},
			wantErr:   context.DeadlineExceeded,
			wantCalls: []authorizers.MCPOperation{authorizers.MCPOperationRead},
		},
		{
			name:      "call error after a read denial is returned",
			errs:      map[authorizers.MCPOperation]error{authorizers.MCPOperationCall: errCall},
			wantErr:   errCall,
			wantCalls: []authorizers.MCPOperation{authorizers.MCPOperationRead, authorizers.MCPOperationCall},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			inner := newFakeOps(t, tc.allowed, tc.errs)
			ctx := context.Background()

			ok, err := authorizers.WithToolCallImpliesRead(inner).AuthorizeWithJWTClaims(
				ctx, authorizers.MCPFeatureTool, authorizers.MCPOperationRead, "weather", nil)

			require.ErrorIs(t, err, tc.wantErr)
			assert.False(t, ok)
			assert.Equal(t, tc.wantCalls, inner.calls)
		})
	}
}

// nativeOps is a fakeOps that also says whether it answers tool:read
// itself, the way the HTTP PDP does when forward_tool_read is set.
type nativeOps struct {
	*fakeOps
	answersRead bool
}

func (a *nativeOps) AnswersToolRead() bool { return a.answersRead }

func TestToolCallImpliesRead_NativeAuthorizer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		answersRead bool
		allowed     []authorizers.MCPOperation
		wantOK      bool
		wantCalls   []authorizers.MCPOperation
	}{
		{
			// Call-only authorizers (the default HTTP PDP) decide visibility with one tool:call check.
			name:        "call-only authorizer is never asked tool:read",
			answersRead: false,
			allowed:     []authorizers.MCPOperation{authorizers.MCPOperationCall},
			wantOK:      true,
			wantCalls:   []authorizers.MCPOperation{authorizers.MCPOperationCall},
		},
		{
			// A read-answering authorizer (forward_tool_read) is asked tool:read once; a deny never falls back to call.
			name:        "read-answering authorizer is asked once and a read deny does not fall back",
			answersRead: true,
			allowed:     []authorizers.MCPOperation{authorizers.MCPOperationCall},
			wantOK:      false,
			wantCalls:   []authorizers.MCPOperation{authorizers.MCPOperationRead},
		},
		{
			name:        "read-answering authorizer read allow is granted without a call check",
			answersRead: true,
			allowed:     []authorizers.MCPOperation{authorizers.MCPOperationRead},
			wantOK:      true,
			wantCalls:   []authorizers.MCPOperation{authorizers.MCPOperationRead},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			inner := &nativeOps{
				fakeOps:     newFakeOps(t, tc.allowed, nil),
				answersRead: tc.answersRead,
			}

			ok, err := authorizers.WithToolCallImpliesRead(inner).AuthorizeWithJWTClaims(
				context.Background(), authorizers.MCPFeatureTool, authorizers.MCPOperationRead, "weather", nil)

			require.NoError(t, err)
			assert.Equal(t, tc.wantOK, ok)
			assert.Equal(t, tc.wantCalls, inner.calls)
		})
	}
}
