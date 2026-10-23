// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authorizers

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// opsAuthorizer allows exactly the operations in allowed, returns errs[operation]
// when set, and records every call it receives.
type opsAuthorizer struct {
	allowed map[MCPOperation]bool
	errs    map[MCPOperation]error
	calls   []MCPOperation
}

func (a *opsAuthorizer) AuthorizeWithJWTClaims(
	_ context.Context,
	_ MCPFeature,
	operation MCPOperation,
	_ string,
	_ map[string]interface{},
) (bool, error) {
	a.calls = append(a.calls, operation)
	if err := a.errs[operation]; err != nil {
		return false, err
	}
	return a.allowed[operation], nil
}

func TestToolCallImpliesRead(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		allowed    []MCPOperation
		feature    MCPFeature
		operation  MCPOperation
		want       bool
		wantCalls  []MCPOperation
		wantMarked bool
	}{
		{
			name:       "tool:read allowed directly is granted without a call check",
			allowed:    []MCPOperation{MCPOperationRead},
			feature:    MCPFeatureTool,
			operation:  MCPOperationRead,
			want:       true,
			wantCalls:  []MCPOperation{MCPOperationRead},
			wantMarked: false,
		},
		{
			name:       "tool:call alone implies tool:read",
			allowed:    []MCPOperation{MCPOperationCall},
			feature:    MCPFeatureTool,
			operation:  MCPOperationRead,
			want:       true,
			wantCalls:  []MCPOperation{MCPOperationRead, MCPOperationCall},
			wantMarked: true,
		},
		{
			name:       "neither tool:read nor tool:call denies",
			allowed:    nil,
			feature:    MCPFeatureTool,
			operation:  MCPOperationRead,
			want:       false,
			wantCalls:  []MCPOperation{MCPOperationRead, MCPOperationCall},
			wantMarked: false,
		},
		{
			name:       "tool:read alone never implies tool:call",
			allowed:    []MCPOperation{MCPOperationRead},
			feature:    MCPFeatureTool,
			operation:  MCPOperationCall,
			want:       false,
			wantCalls:  []MCPOperation{MCPOperationCall},
			wantMarked: false,
		},
		{
			name:       "tool:call passes through unchanged",
			allowed:    []MCPOperation{MCPOperationCall},
			feature:    MCPFeatureTool,
			operation:  MCPOperationCall,
			want:       true,
			wantCalls:  []MCPOperation{MCPOperationCall},
			wantMarked: false,
		},
		{
			name:       "non-tool read is not rewritten to a tool:call check",
			allowed:    []MCPOperation{MCPOperationCall},
			feature:    MCPFeatureResource,
			operation:  MCPOperationRead,
			want:       false,
			wantCalls:  []MCPOperation{MCPOperationRead},
			wantMarked: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			inner := &opsAuthorizer{allowed: map[MCPOperation]bool{}}
			for _, op := range tc.allowed {
				inner.allowed[op] = true
			}

			a := WithToolCallImpliesRead(inner)
			marker := &DiscoveryMarker{}
			ctx := WithDiscoveryMarker(context.Background(), marker)
			got, err := a.AuthorizeWithJWTClaims(ctx, tc.feature, tc.operation, "weather", nil)

			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
			assert.Equal(t, tc.wantCalls, inner.calls)
			assert.Equal(t, tc.wantMarked, marker.FallbackCount > 0)
		})
	}
}

func TestToolCallImpliesRead_ErrorPaths(t *testing.T) {
	t.Parallel()

	errRead := errors.New("PDP unavailable")
	errCall := errors.New("call check failed")

	tests := []struct {
		name      string
		allowed   []MCPOperation
		errs      map[MCPOperation]error
		wantErr   error
		wantCalls []MCPOperation
	}{
		{
			name:      "read error is returned without a call check",
			allowed:   []MCPOperation{MCPOperationCall},
			errs:      map[MCPOperation]error{MCPOperationRead: errRead},
			wantErr:   errRead,
			wantCalls: []MCPOperation{MCPOperationRead},
		},
		{
			name:      "read timeout is returned without a call check",
			allowed:   []MCPOperation{MCPOperationCall},
			errs:      map[MCPOperation]error{MCPOperationRead: context.DeadlineExceeded},
			wantErr:   context.DeadlineExceeded,
			wantCalls: []MCPOperation{MCPOperationRead},
		},
		{
			name:      "call error after a read denial is returned",
			errs:      map[MCPOperation]error{MCPOperationCall: errCall},
			wantErr:   errCall,
			wantCalls: []MCPOperation{MCPOperationRead, MCPOperationCall},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			inner := &opsAuthorizer{allowed: map[MCPOperation]bool{}, errs: tc.errs}
			for _, op := range tc.allowed {
				inner.allowed[op] = true
			}
			marker := &DiscoveryMarker{}
			ctx := WithDiscoveryMarker(context.Background(), marker)

			ok, err := WithToolCallImpliesRead(inner).AuthorizeWithJWTClaims(
				ctx, MCPFeatureTool, MCPOperationRead, "weather", nil)

			require.ErrorIs(t, err, tc.wantErr)
			assert.False(t, ok)
			assert.Equal(t, tc.wantCalls, inner.calls)
			assert.Zero(t, marker.FallbackCount)
		})
	}
}
