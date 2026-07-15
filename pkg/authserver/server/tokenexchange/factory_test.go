// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokenexchange

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFactory(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name               string
		delegationLifespan time.Duration
		wantErr            bool
	}{
		{
			name:               "zero delegationLifespan returns error",
			delegationLifespan: 0,
			wantErr:            true,
		},
		{
			name:               "negative delegationLifespan returns error",
			delegationLifespan: -time.Minute,
			wantErr:            true,
		},
		{
			name:               "positive delegationLifespan succeeds",
			delegationLifespan: 15 * time.Minute,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f, err := Factory(tt.delegationLifespan)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "delegationLifespan must be positive")
				assert.Nil(t, f)
				return
			}
			require.NoError(t, err)
			assert.NotNil(t, f)
		})
	}
}
