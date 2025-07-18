//nolint:paralleltest // reason: This test must run sequentially due to shared state
package app

import (
	"bytes"
	"context"
	"sync"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"

	"github.com/stacklok/toolhive/pkg/groups"
)

var (
	testRootCmdOnce sync.Once
	testRootCmd     *cobra.Command
)

func getTestRootCmd() *cobra.Command {
	testRootCmdOnce.Do(func() {
		testRootCmd = NewRootCmd(false)
	})
	return testRootCmd
}

func createGroupViaCLI(cmd *cobra.Command, groupName string) error {
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"group", "create", groupName})
	return cmd.Execute()
}

func TestGroupCreateCmd(t *testing.T) {
	cmd := getTestRootCmd()
	groupName := "testgroup_cli_" + t.Name()

	// Cleanup after test
	defer func() {
		// Create a manager to clean up the group
		cleanupManager, cleanupErr := groups.NewManager()
		if cleanupErr == nil {
			ctx := context.Background()
			_ = cleanupManager.Delete(ctx, groupName)
		}
	}()

	err := createGroupViaCLI(cmd, groupName)
	assert.NoError(t, err)
}

func TestGroupCreateCmd_Duplicate(t *testing.T) {
	cmd := getTestRootCmd()
	groupName := "dupegroup_cli_" + t.Name()

	// Cleanup after test
	defer func() {
		// Create a manager to clean up the group
		cleanupManager, cleanupErr := groups.NewManager()
		if cleanupErr == nil {
			ctx := context.Background()
			_ = cleanupManager.Delete(ctx, groupName)
		}
	}()

	err := createGroupViaCLI(cmd, groupName)
	assert.NoError(t, err)

	// Try to create the same group again
	err = createGroupViaCLI(cmd, groupName)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
}

func TestGroupCreateCmd_MissingArg(t *testing.T) {
	cmd := getTestRootCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"group", "create"})
	err := cmd.Execute()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "accepts 1 arg(s)")
}
