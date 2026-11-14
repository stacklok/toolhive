// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAIPluginSyncTrustFlags pins the flags that carry explicit adoption trust
// decisions into plugins.SyncOptions.
//
//nolint:paralleltest // sets the command's package-level flag variable
func TestAIPluginSyncTrustFlags(t *testing.T) {
	unsignedFlag := aiPluginSyncCmd.Flags().Lookup("allow-unsigned")
	require.NotNil(t, unsignedFlag, "thv ai-plugin sync must expose --allow-unsigned")
	assert.Equal(t, "false", unsignedFlag.DefValue, "unsigned adoption must never be the default")
	publicKeyFlag := aiPluginSyncCmd.Flags().Lookup("public-key")
	require.NotNil(t, publicKeyFlag, "thv ai-plugin sync must expose --public-key")
	assert.Empty(t, publicKeyFlag.DefValue)

	t.Cleanup(func() {
		aiPluginSyncAllowUnsigned = false
		aiPluginSyncPublicKey = ""
		require.NoError(t, unsignedFlag.Value.Set("false"))
		require.NoError(t, publicKeyFlag.Value.Set(""))
		unsignedFlag.Changed = false
		publicKeyFlag.Changed = false
	})
	require.NoError(t, unsignedFlag.Value.Set("true"))
	require.NoError(t, publicKeyFlag.Value.Set("cosign.pub"))
	assert.True(t, aiPluginSyncAllowUnsigned,
		"--allow-unsigned must bind to the variable sync passes to the service")
	assert.Equal(t, "cosign.pub", aiPluginSyncPublicKey,
		"--public-key must bind to the path sync reads and encodes before the HTTP request")
}
