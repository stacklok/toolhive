// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUnknownFlagError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		err      error
		wantHint bool
	}{
		{
			name:     "unknown long flag gets the hint",
			err:      errors.New("unknown flag: --permission-profile"),
			wantHint: true,
		},
		{
			name:     "unknown shorthand flag gets the hint",
			err:      errors.New("unknown shorthand flag: 'p' in -p"),
			wantHint: true,
		},
		{
			name:     "mutually exclusive flags are returned unchanged",
			err:      errors.New("if any flags in the group [all group] are set none of the others can be"),
			wantHint: false,
		},
		{
			name:     "unrelated error is returned unchanged",
			err:      errors.New("some other failure"),
			wantHint: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := unknownFlagError(tt.err)
			if tt.wantHint {
				require.ErrorIs(t, got, tt.err)
				assert.Contains(t, got.Error(), "Hint:")
			} else {
				assert.Equal(t, tt.err, got)
			}
		})
	}
}
