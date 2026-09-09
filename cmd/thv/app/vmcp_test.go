// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewVMCPInitCommand_Flags(t *testing.T) {
	t.Parallel()

	cmd := newVMCPInitCommand()

	groupFlag := cmd.Flags().Lookup("group")
	require.NotNil(t, groupFlag, "expected --group flag to be registered")
	assert.Equal(t, "g", groupFlag.Shorthand)
	assert.Equal(t, "", groupFlag.DefValue)

	outputFlag := cmd.Flags().Lookup("output")
	require.NotNil(t, outputFlag, "expected --output flag to be registered")
	assert.Equal(t, "o", outputFlag.Shorthand)
	assert.Equal(t, "", outputFlag.DefValue)

	configFlag := cmd.Flags().Lookup("config")
	require.NotNil(t, configFlag, "expected --config flag to be registered")
	assert.Equal(t, "c", configFlag.Shorthand)
	assert.Equal(t, "", configFlag.DefValue)
}

func TestNewVMCPInitCommand_GroupRequired(t *testing.T) {
	t.Parallel()

	cmd := newVMCPInitCommand()
	// Execute with no flags: Cobra should reject before RunE is called.
	cmd.SetArgs([]string{})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "group")
}

func TestNewVMCPCommand_InitRegistered(t *testing.T) {
	t.Parallel()

	cmd := newVMCPCommand()

	var found bool
	for _, sub := range cmd.Commands() {
		if sub.Use == "init" {
			found = true
			break
		}
	}
	assert.True(t, found, "expected 'init' to be registered as a subcommand of 'vmcp'")
}

func TestNewVMCPValidateCommand_FormatFlag(t *testing.T) {
	t.Parallel()

	cmd := newVMCPValidateCommand()
	formatFlag := cmd.Flags().Lookup("format")
	require.NotNil(t, formatFlag, "expected --format flag to be registered")
	assert.Equal(t, FormatText, formatFlag.DefValue)

	require.NoError(t, formatFlag.Value.Set("yaml"))
	err := cmd.PreRunE(cmd, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `invalid format "yaml"`)
}

func TestNewVMCPValidateCommand_JSONOutput(t *testing.T) {
	t.Parallel()

	configPath := filepath.Join(t.TempDir(), "vmcp.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte(`
name: cli-json-vmcp
groupRef: cli-json-group
incomingAuth:
  type: anonymous
outgoingAuth:
  source: inline
  default:
    type: unauthenticated
aggregation:
  conflictResolution: prefix
  conflictResolutionConfig:
    prefixFormat: "{workload}_"
`), 0o600))

	cmd := newVMCPValidateCommand()
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetArgs([]string{"--config", configPath, "--format", FormatJSON})
	require.NoError(t, cmd.Execute())

	var summary map[string]any
	require.NoError(t, json.Unmarshal(output.Bytes(), &summary))
	assert.Equal(t, true, summary["valid"])
	assert.Equal(t, "cli-json-vmcp", summary["name"])
}
