// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokenexchange

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestActorDeniedErrorError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  *ActorDeniedError
		want string
	}{
		{
			name: "reason set",
			err:  &ActorDeniedError{Reason: ActorDeniedReasonSVIDExpired},
			want: "actor denied: svid_expired",
		},
		{
			name: "empty reason",
			err:  &ActorDeniedError{},
			want: "actor denied",
		},
		{
			name: "nil receiver",
			err:  nil,
			want: "actor denied",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, tt.err.Error())
		})
	}
}

func TestActorDeniedErrorUnwrapsWithErrorsAs(t *testing.T) {
	t.Parallel()

	wrapped := fmt.Errorf("validating actor: %w", &ActorDeniedError{Reason: ActorDeniedReasonApprovalRequired})

	var denied *ActorDeniedError
	require.True(t, errors.As(wrapped, &denied))
	assert.Equal(t, ActorDeniedReasonApprovalRequired, denied.Reason)
}

func TestActorDeniedReasonValues(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "svid_expired", string(ActorDeniedReasonSVIDExpired))
	assert.Equal(t, "approval_required", string(ActorDeniedReasonApprovalRequired))
	assert.Equal(t, "denied", string(ActorDeniedReasonDenied))
}
