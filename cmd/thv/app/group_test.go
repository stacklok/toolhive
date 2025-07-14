package app

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
)

func createGroupViaCLI(cmd *cobra.Command, groupName string) error {
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"group", "create", groupName})
	return cmd.Execute()
}

func TestGroupCreateCmd(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	os.Setenv("XDG_STATE_HOME", tempDir)

	cmd := rootCmd
	groupName := "testgroup"

	err := createGroupViaCLI(cmd, groupName)
	assert.NoError(t, err)

	// Check that the group file exists in the state dir
	groupFile := filepath.Join(tempDir, "toolhive", "groups", groupName+".json")
	_, statErr := os.Stat(groupFile)
	assert.NoError(t, statErr, "Group file should be created")
}

func TestGroupCreateCmd_Duplicate(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	os.Setenv("XDG_STATE_HOME", tempDir)

	cmd := rootCmd
	groupName := "dupegroup"

	err := createGroupViaCLI(cmd, groupName)
	assert.NoError(t, err)

	// Try to create the same group again
	err = createGroupViaCLI(cmd, groupName)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
}

func TestGroupCreateCmd_MissingArg(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	os.Setenv("XDG_STATE_HOME", tempDir)

	cmd := rootCmd
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"group", "create"})
	err := cmd.Execute()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "accepts 1 arg(s)")
}
