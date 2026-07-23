// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/skills"
)

func TestSyncExitError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		result   *skills.SyncResult
		check    bool
		wantCode int
	}{
		{name: "clean sync", result: &skills.SyncResult{AlreadyCurrent: []string{"a"}}, wantCode: 0},
		{
			name:     "check finds drift",
			result:   &skills.SyncResult{Drifted: []string{"a"}},
			check:    true,
			wantCode: ExitCodeCheckFailure,
		},
		{
			// The fresh-clone CI gate: a lock entry with no install record
			// at all must fail --check exactly like drift does.
			name:     "check finds missing installs",
			result:   &skills.SyncResult{Missing: []string{"a"}},
			check:    true,
			wantCode: ExitCodeCheckFailure,
		},
		{
			name:     "non-check drift is not a failure (already reinstalled)",
			result:   &skills.SyncResult{Drifted: []string{"a"}, Installed: []string{"a"}},
			check:    false,
			wantCode: 0,
		},
		{
			name:     "non-check missing is not a failure (already installed)",
			result:   &skills.SyncResult{Missing: []string{"a"}, Installed: []string{"a"}},
			check:    false,
			wantCode: 0,
		},
		{
			name:     "any failure is a partial failure",
			result:   &skills.SyncResult{Failed: []skills.SyncFailure{{Name: "a", Error: "boom"}}},
			wantCode: ExitCodePartialFailure,
		},
		{
			name: "failure takes precedence over check drift",
			result: &skills.SyncResult{
				Drifted: []string{"a"},
				Failed:  []skills.SyncFailure{{Name: "b", Error: "boom"}},
			},
			check:    true,
			wantCode: ExitCodePartialFailure,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := syncExitError(tt.result, tt.check)
			assert.Equal(t, tt.wantCode, ExitCodeFromError(err))
		})
	}
}

func TestIsSyncResultEmpty(t *testing.T) {
	t.Parallel()

	assert.True(t, isSyncResultEmpty(&skills.SyncResult{}))
	assert.False(t, isSyncResultEmpty(&skills.SyncResult{Installed: []string{"a"}}))
	assert.False(t, isSyncResultEmpty(&skills.SyncResult{Failed: []skills.SyncFailure{{Name: "a"}}}))
}

func TestPrintSyncResultJSON(t *testing.T) {
	t.Parallel()
	err := printSyncResult(&skills.SyncResult{Installed: []string{"my-skill"}}, FormatJSON)
	require.NoError(t, err)
}

func TestPrintSyncResultText(t *testing.T) {
	t.Parallel()

	t.Run("every category populated", func(t *testing.T) {
		t.Parallel()
		err := printSyncResult(&skills.SyncResult{
			Installed:       []string{"installed-skill"},
			Drifted:         []string{"drifted-skill"},
			AlreadyCurrent:  []string{"current-skill"},
			NeverManaged:    []string{"unmanaged-skill"},
			RemovedFromLock: []string{"removed-skill"},
			Pruned:          []string{"pruned-skill"},
			Failed:          []skills.SyncFailure{{Name: "failed-skill", Reason: skills.FailureReasonUnknown, Error: "boom"}},
		}, FormatText)
		require.NoError(t, err)
	})

	t.Run("empty result prints nothing-to-sync", func(t *testing.T) {
		t.Parallel()
		err := printSyncResult(&skills.SyncResult{}, FormatText)
		require.NoError(t, err)
	})
}
