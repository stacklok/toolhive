// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/container/kubernetes"
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

	t.Setenv(kubernetes.EnvVarMCPServerGeneration, "3")

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

// TestApplyMCPServerGenerationOverride exercises the override helper in
// isolation, covering the defensive-validation branches: empty env (no-op),
// unparseable env (fall through), negative env (fall through), and the
// happy path. metadata.generation is a monotonic non-negative integer per
// the K8s API convention, so a negative value cannot have come from a
// legitimate downward-API projection and must not be allowed to silently
// disable the apply-gate stamp.
func TestApplyMCPServerGenerationOverride(t *testing.T) {
	tests := []struct {
		name     string
		envValue string // "" means don't set
		fileGen  int64
		wantGen  int64
	}{
		{name: "env unset preserves file value", envValue: "", fileGen: 5, wantGen: 5},
		{name: "valid env overrides file", envValue: "3", fileGen: 5, wantGen: 3},
		{name: "zero env overrides file (caller's choice)", envValue: "0", fileGen: 5, wantGen: 0},
		{name: "unparseable env preserves file value", envValue: "not-a-number", fileGen: 5, wantGen: 5},
		{name: "negative env preserves file value", envValue: "-1", fileGen: 5, wantGen: 5},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.envValue != "" {
				t.Setenv(kubernetes.EnvVarMCPServerGeneration, tc.envValue)
			} else {
				// Explicitly clear so a stray env from the host doesn't leak.
				t.Setenv(kubernetes.EnvVarMCPServerGeneration, "")
			}
			cfg := &runner.RunConfig{MCPServerGeneration: tc.fileGen}
			applyMCPServerGenerationOverride(cfg)
			assert.Equal(t, tc.wantGen, cfg.MCPServerGeneration)
		})
	}
}
