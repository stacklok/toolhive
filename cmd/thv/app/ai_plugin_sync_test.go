// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAIPluginSyncAllowUnsignedFlag pins the flag that carries the unsigned
// trust decision into sync: adopting an install whose signature state cannot
// be established is rejected unless the user opts in here, and the flag must
// be bound to the variable the command threads into plugins.SyncOptions.
//
//nolint:paralleltest // sets the command's package-level flag variable
func TestAIPluginSyncAllowUnsignedFlag(t *testing.T) {
	flag := aiPluginSyncCmd.Flags().Lookup("allow-unsigned")
	require.NotNil(t, flag, "thv ai-plugin sync must expose --allow-unsigned")
	assert.Equal(t, "false", flag.DefValue, "unsigned adoption must never be the default")

	t.Cleanup(func() { aiPluginSyncAllowUnsigned = false })
	require.NoError(t, flag.Value.Set("true"))
	assert.True(t, aiPluginSyncAllowUnsigned,
		"--allow-unsigned must bind to the variable sync passes to the service")
}
