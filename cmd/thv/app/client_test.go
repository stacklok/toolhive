package app

import (
	"bytes"
	"io"
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

func TestClientRegisterCmd(t *testing.T) { //nolint:paralleltest // Uses environment variables
	tempDir := t.TempDir()
	os.Setenv("XDG_CONFIG_HOME", tempDir)

	cmd := rootCmd

	err := registerClientViaCLI(cmd, "vscode")
	assert.NoError(t, err)

	cfg := config.GetConfig()
	assert.Contains(t, cfg.Clients.RegisteredClients, "vscode", "Client should be registered")
}

func TestClientRemoveCmd(t *testing.T) { //nolint:paralleltest // Uses environment variables
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

func TestClientRegisterCmd_InvalidClient(t *testing.T) { //nolint:paralleltest // Uses environment variables
	tempDir := t.TempDir()
	os.Setenv("XDG_CONFIG_HOME", tempDir)

	cmd := rootCmd

	err := registerClientViaCLI(cmd, "not-a-client")
	assert.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "invalid client type"))
}

func TestClientRemoveCmd_InvalidClient(t *testing.T) { //nolint:paralleltest // Uses environment variables
	tempDir := t.TempDir()
	os.Setenv("XDG_CONFIG_HOME", tempDir)

	cmd := rootCmd

	err := removeClientViaCLI(cmd, "not-a-client")
	assert.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "invalid client type"))
}

func TestListRegisteredClientsCmd_Sorting(t *testing.T) { //nolint:paralleltest // Uses environment variables
	tempDir := t.TempDir()
	os.Setenv("XDG_CONFIG_HOME", tempDir)

	// Pre-populate config with multiple registered clients in non-alphabetical order
	testClients := []string{"vscode", "cursor", "roo-code", "cline", "claude-code"}
	err := config.UpdateConfig(func(c *config.Config) {
		c.Clients.RegisteredClients = testClients
	})
	assert.NoError(t, err)

	// Temporarily redirect stdout to capture the output
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	// Call the function directly
	err = listRegisteredClientsCmdFunc(nil, nil)
	assert.NoError(t, err)

	// Restore stdout and read the captured output
	w.Close()
	os.Stdout = oldStdout

	outputBytes, err := io.ReadAll(r)
	assert.NoError(t, err)
	outputStr := string(outputBytes)

	// Extract client names from output (they appear after "- ")
	lines := strings.Split(outputStr, "\n")
	var foundClients []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "- ") {
			client := strings.TrimPrefix(line, "- ")
			foundClients = append(foundClients, client)
		}
	}

	// Verify all clients are present
	assert.Len(t, foundClients, len(testClients), "Should find all registered clients")
	for _, expectedClient := range testClients {
		assert.Contains(t, foundClients, expectedClient, "Should contain client: %s", expectedClient)
	}

	// Verify alphabetical order
	for i := 1; i < len(foundClients); i++ {
		assert.True(t, foundClients[i-1] < foundClients[i],
			"Clients should be sorted alphabetically: %s should come before %s",
			foundClients[i-1], foundClients[i])
	}
}
