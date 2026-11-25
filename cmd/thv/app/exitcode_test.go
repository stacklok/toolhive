// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestExitCodeFromError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want int
	}{
		{name: "nil error", err: nil, want: 0},
		{name: "generic error", err: errors.New("boom"), want: 1},
		{name: "check failure", err: withExitCode(errors.New("drift"), ExitCodeCheckFailure), want: 2},
		{name: "partial failure", err: withExitCode(errors.New("partial"), ExitCodePartialFailure), want: 3},
		{name: "policy rejection", err: withExitCode(errors.New("refused"), ExitCodePolicyRejection), want: 4},
		{
			name: "wrapped exit code error is still detected",
			err:  fmt.Errorf("context: %w", withExitCode(errors.New("drift"), ExitCodeCheckFailure)),
			want: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, ExitCodeFromError(tt.err))
		})
	}
}

func TestWithExitCodeNilIsNil(t *testing.T) {
	t.Parallel()
	assert.NoError(t, withExitCode(nil, ExitCodeCheckFailure))
}

func TestExitCodeErrorUnwraps(t *testing.T) {
	t.Parallel()
	inner := errors.New("boom")
	err := withExitCode(inner, ExitCodePartialFailure)
	assert.ErrorIs(t, err, inner)
	assert.Equal(t, inner.Error(), err.Error())
}
