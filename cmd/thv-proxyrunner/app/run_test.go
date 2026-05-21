// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/runner"
)

// TestTryLoadConfigFromFile_MCPServerGenerationEnvOverride asserts that when
// THV_MCPSERVER_GENERATION is set in the environment, the proxyrunner's loaded
// RunConfig carries that value rather than the one from runconfig.json.
//
// Issue #5360: the /etc/runconfig ConfigMap volume is mounted live (no
// subPath), so a restarted old-RS proxyrunner pod re-reads the file after a
// helm upgrade and picks up the new MCPServer.metadata.generation. Both old
// and new pods then call DeployWorkload with the same ourGen, defeating the
// strict-greater-than gate at pkg/container/kubernetes/client.go:530.
//
// The fix sources MCPServerGeneration from an env var injected via the
// downward API (frozen at pod creation, parallel to how the image is frozen
// via the CLI positional arg). This test exercises that override and fails
// today because no such override exists in the config-loading path.
func TestTryLoadConfigFromFile_MCPServerGenerationEnvOverride(t *testing.T) {
	// Skip when system-wide runconfig paths exist; tryLoadConfigFromFile
	// checks them ahead of ./runconfig.json. Avoids false positives on
	// machines where toolhive runtime data happens to live in /etc.
	for _, p := range []string{kubernetesRunConfigPath, systemRunConfigPath} {
		if _, err := os.Stat(p); err == nil {
			t.Skipf("skipping: %s exists and would shadow the test fixture", p)
		}
	}

	dir := t.TempDir()
	t.Chdir(dir)

	cfg := &runner.RunConfig{
		SchemaVersion:       runner.CurrentSchemaVersion,
		MCPServerGeneration: 5,
	}
	data, err := json.Marshal(cfg)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile("runconfig.json", data, 0o600))

	t.Setenv("THV_MCPSERVER_GENERATION", "3")

	loaded, err := tryLoadConfigFromFile()
	require.NoError(t, err)
	require.NotNil(t, loaded)

	assert.Equal(t, int64(3), loaded.MCPServerGeneration,
		"THV_MCPSERVER_GENERATION must override the file value; without "+
			"a frozen-per-pod source for MCPServerGeneration the apply-gate "+
			"at pkg/container/kubernetes/client.go:530 cannot distinguish "+
			"two proxyrunner pods that have both re-read the live-mounted "+
			"ConfigMap (issue #5360)")
}
