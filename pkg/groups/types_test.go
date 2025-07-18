package groups

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/state"
)

func TestManager_Create(t *testing.T) {
	t.Parallel()
	manager, err := NewManager()
	require.NoError(t, err)

	ctx := context.Background()
	groupName := "testgroup_create_" + t.Name()

	// Cleanup after test
	defer func() {
		_ = manager.Delete(ctx, groupName)
	}()

	// Test creating a new group
	err = manager.Create(ctx, groupName)
	assert.NoError(t, err)

	// Test creating the same group again (should fail)
	err = manager.Create(ctx, groupName)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
}

func TestManager_Get(t *testing.T) {
	t.Parallel()
	manager, err := NewManager()
	require.NoError(t, err)

	ctx := context.Background()
	groupName := "testgroup_get_" + t.Name()

	// Cleanup after test
	defer func() {
		_ = manager.Delete(ctx, groupName)
	}()

	// Create a group first
	err = manager.Create(ctx, groupName)
	require.NoError(t, err)

	// Test getting the group
	group, err := manager.Get(ctx, groupName)
	assert.NoError(t, err)
	assert.Equal(t, groupName, group.Name)

	// Test getting a non-existent group
	_, err = manager.Get(ctx, "nonexistent")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestManager_List(t *testing.T) {
	t.Parallel()
	manager, err := NewManager()
	require.NoError(t, err)

	ctx := context.Background()

	// Create some groups with unique names
	groupNames := []string{
		"group1_list_" + t.Name(),
		"group2_list_" + t.Name(),
		"group3_list_" + t.Name(),
	}

	// Cleanup after test
	defer func() {
		for _, name := range groupNames {
			_ = manager.Delete(ctx, name)
		}
	}()

	for _, name := range groupNames {
		err = manager.Create(ctx, name)
		require.NoError(t, err)
	}

	// Test listing all groups
	groups, err := manager.List(ctx)
	assert.NoError(t, err)
	assert.Len(t, groups, 3)

	// Check that all groups are present
	groupMap := make(map[string]bool)
	for _, group := range groups {
		groupMap[group.Name] = true
	}
	for _, name := range groupNames {
		assert.True(t, groupMap[name], "Group %s should be in the list", name)
	}
}

func TestManager_Delete(t *testing.T) {
	t.Parallel()
	manager, err := NewManager()
	require.NoError(t, err)

	ctx := context.Background()
	groupName := "testgroup_delete_" + t.Name()

	// Create a group first
	err = manager.Create(ctx, groupName)
	require.NoError(t, err)

	// Verify it exists
	exists, err := manager.Exists(ctx, groupName)
	assert.NoError(t, err)
	assert.True(t, exists)

	// Test deleting the group
	err = manager.Delete(ctx, groupName)
	assert.NoError(t, err)

	// Verify it no longer exists
	exists, err = manager.Exists(ctx, groupName)
	assert.NoError(t, err)
	assert.False(t, exists)

	// Test deleting a non-existent group
	err = manager.Delete(ctx, "nonexistent")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestManager_Exists(t *testing.T) {
	t.Parallel()
	manager, err := NewManager()
	require.NoError(t, err)

	ctx := context.Background()
	groupName := "testgroup_exists_" + t.Name()

	// Cleanup after test
	defer func() {
		_ = manager.Delete(ctx, groupName)
	}()

	// Test checking non-existent group
	exists, err := manager.Exists(ctx, groupName)
	assert.NoError(t, err)
	assert.False(t, exists)

	// Create the group
	err = manager.Create(ctx, groupName)
	require.NoError(t, err)

	// Test checking existing group
	exists, err = manager.Exists(ctx, groupName)
	assert.NoError(t, err)
	assert.True(t, exists)
}

func TestGroup_WriteJSON(t *testing.T) {
	t.Parallel()

	group := &Group{Name: "testgroup"}

	// Create a temporary file
	tmpFile, err := os.CreateTemp("", "group_test")
	require.NoError(t, err)
	defer os.Remove(tmpFile.Name())
	defer tmpFile.Close()

	// Write the group to JSON
	err = group.WriteJSON(tmpFile)
	assert.NoError(t, err)

	// Read the file content
	content, err := os.ReadFile(tmpFile.Name())
	assert.NoError(t, err)

	// Verify the JSON content
	expected := `{
  "name": "testgroup"
}
`
	assert.Equal(t, expected, string(content))
}

// TestManager_DirectStateStore tests the manager with a direct state store path
func TestManager_DirectStateStore(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()

	// Create the groups directory structure manually
	groupsDir := filepath.Join(tempDir, "toolhive", "groups")
	err := os.MkdirAll(groupsDir, 0750)
	require.NoError(t, err)

	// Create a manager with a custom state store
	store, err := state.NewLocalStore("toolhive", "groups")
	require.NoError(t, err)

	// Create a manager directly with the store
	manager := &manager{store: store}

	ctx := context.Background()
	groupName := "directtest_" + t.Name()

	// Cleanup after test
	defer func() {
		_ = manager.Delete(ctx, groupName)
	}()

	// Test creating a new group
	err = manager.Create(ctx, groupName)
	assert.NoError(t, err)

	// Test getting the group
	group, err := manager.Get(ctx, groupName)
	assert.NoError(t, err)
	assert.Equal(t, groupName, group.Name)

	// Test that the group exists
	exists, err := manager.Exists(ctx, groupName)
	assert.NoError(t, err)
	assert.True(t, exists)
}
