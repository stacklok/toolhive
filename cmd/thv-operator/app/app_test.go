// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIsStorageVersionMigratorEnabled exercises the env-var contract for the
// StorageVersionMigrator feature flag. The function must:
//   - default to (false, nil) when the env var is unset (the controller is
//     opt-in for this release),
//   - accept strconv.ParseBool's full truth-table for explicit values,
//   - fail loudly on unparseable values so a misconfigured admin sees a
//     startup error instead of silently disabled migration.
//
// The error-case rows assert on both the env-var name AND the offending
// value being present in the error message, so a future refactor that
// drops either fragment fails this test.
func TestIsStorageVersionMigratorEnabled(t *testing.T) {
	// Intentionally NOT t.Parallel(): subtests use t.Setenv, which
	// panics if the test (or any ancestor) is parallel. Subtests are
	// also serial for the same reason. The trade-off is negligible —
	// the test is sub-millisecond — and matches Go 1.20+ guidance for
	// env-var-driven tests.

	tests := []struct {
		name        string
		setEnv      bool
		envValue    string
		wantEnabled bool
		wantErr     bool
	}{
		{
			name:        "unset defaults to disabled",
			setEnv:      false,
			wantEnabled: false,
			wantErr:     false,
		},
		{
			name:        "explicit true enables",
			setEnv:      true,
			envValue:    "true",
			wantEnabled: true,
			wantErr:     false,
		},
		{
			name:        "explicit false disables",
			setEnv:      true,
			envValue:    "false",
			wantEnabled: false,
			wantErr:     false,
		},
		{
			name:        "explicit 1 enables (ParseBool truthy)",
			setEnv:      true,
			envValue:    "1",
			wantEnabled: true,
			wantErr:     false,
		},
		{
			name:        "explicit 0 disables (ParseBool falsy)",
			setEnv:      true,
			envValue:    "0",
			wantEnabled: false,
			wantErr:     false,
		},
		{
			name:        "unparseable typo errors with env-var name and bad value",
			setEnv:      true,
			envValue:    "ture",
			wantEnabled: false,
			wantErr:     true,
		},
		{
			name:        "empty string errors (ParseBool rejects empty)",
			setEnv:      true,
			envValue:    "",
			wantEnabled: false,
			wantErr:     true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Subtests intentionally do NOT call t.Parallel(): t.Setenv
			// is incompatible with a parallel *testing.T (it panics with
			// "test using t.Setenv ... can not use t.Parallel"). The
			// outer test still runs in parallel with other tests in the
			// package — only sibling subtests of this test serialize,
			// which is fine because t.Setenv restores the prior value
			// at the subtest's end.
			if tc.setEnv {
				t.Setenv(envEnableStorageVersionMigrator, tc.envValue)
			}

			got, err := isStorageVersionMigratorEnabled()

			if tc.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), envEnableStorageVersionMigrator,
					"error message must name the env var so admins can find the misconfiguration")
				assert.Contains(t, err.Error(), `"`+tc.envValue+`"`,
					"error message must quote the offending value so admins can spot typos")
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tc.wantEnabled, got)
		})
	}
}
