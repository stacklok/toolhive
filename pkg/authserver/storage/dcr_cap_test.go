// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package storage

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunConfig_ResolvedMaxDCRClients(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		config  *RunConfig
		want    int
		wantErr bool
	}{
		{name: "nil config uses the default", want: DefaultMaxDCRClients},
		{name: "unset uses the default", config: &RunConfig{}, want: 10000},
		{name: "zero means unlimited", config: &RunConfig{MaxDCRClients: new(0)}, want: 0},
		{name: "configured value", config: &RunConfig{MaxDCRClients: new(25)}, want: 25},
		{name: "negative rejected", config: &RunConfig{MaxDCRClients: new(-1)}, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := tc.config.ResolvedMaxDCRClients()
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestClientCapacityError(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	err := fmt.Errorf("register: %w", newClientCapacityError(7, now.Add(time.Minute), now))
	require.ErrorIs(t, err, ErrClientCapacity)
	var capErr *ClientCapacityError
	require.True(t, errors.As(err, &capErr))
	assert.Equal(t, time.Minute, capErr.RetryAfter)
	assert.Equal(t, 7, capErr.Cap)

	assert.Zero(t, newClientCapacityError(7, now.Add(-time.Minute), now).RetryAfter,
		"an expiry in the past yields no wait")
}
