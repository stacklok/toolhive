// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokenexchange

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver/server"
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
		{
			name:               "delegationLifespan at max access token lifespan succeeds",
			delegationLifespan: server.MaxAccessTokenLifespan,
		},
		{
			name:               "delegationLifespan above max access token lifespan returns error",
			delegationLifespan: server.MaxAccessTokenLifespan + time.Hour,
			wantErr:            true,
		},
		{
			name:               "delegationLifespan of 48h returns error",
			delegationLifespan: 48 * time.Hour,
			wantErr:            true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f, err := Factory(tt.delegationLifespan)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "delegationLifespan must be between")
				assert.Nil(t, f)
				return
			}
			require.NoError(t, err)
			assert.NotNil(t, f)
		})
	}
}
