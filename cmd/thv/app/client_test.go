package app

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"

	"github.com/stacklok/toolhive/pkg/config"
)

func registerClientViaCLI(cmd *cobra.Command, client string) error {
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"client", "register", client})
	return cmd.Execute()
}

func removeClientViaCLI(cmd *cobra.Command, client string) error {
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"client", "remove", client})
	return cmd.Execute()
}

func TestClientRegisterCmd(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	os.Setenv("XDG_CONFIG_HOME", tempDir)

	cmd := rootCmd

	err := registerClientViaCLI(cmd, "vscode")
	assert.NoError(t, err)

	cfg := config.GetConfig()
	assert.Contains(t, cfg.Clients.RegisteredClients, "vscode", "Client should be registered")
}

func TestClientRemoveCmd(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	os.Setenv("XDG_CONFIG_HOME", tempDir)

	// Pre-populate config with a registered client
	err := config.UpdateConfig(func(c *config.Config) {
		c.Clients.RegisteredClients = []string{"vscode"}
	})
	assert.NoError(t, err)

	cmd := rootCmd

	err = removeClientViaCLI(cmd, "vscode")
	assert.NoError(t, err)

	cfg := config.GetConfig()
	assert.NotContains(t, cfg.Clients.RegisteredClients, "vscode", "Client should be removed")
}

func TestClientRegisterCmd_InvalidClient(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	os.Setenv("XDG_CONFIG_HOME", tempDir)

	cmd := rootCmd

	err := registerClientViaCLI(cmd, "not-a-client")
	assert.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "invalid client type"))
}

func TestClientRemoveCmd_InvalidClient(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	os.Setenv("XDG_CONFIG_HOME", tempDir)

	cmd := rootCmd

	err := removeClientViaCLI(cmd, "not-a-client")
	assert.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "invalid client type"))
}
