// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAIPluginInstallAllowUnsignedFlag pins the flag that carries the
// unsigned-install trust decision: an install of a project-scoped plugin
// without a verified signature is rejected unless the user opts in here.
func TestAIPluginInstallAllowUnsignedFlag(t *testing.T) {
	t.Parallel()

	flag := aiPluginInstallCmd.Flags().Lookup("allow-unsigned")
	require.NotNil(t, flag, "thv ai-plugin install must expose --allow-unsigned")
	assert.Equal(t, "false", flag.DefValue, "unsigned installs must never be the default")
}
