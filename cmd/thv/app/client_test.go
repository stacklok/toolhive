package app

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"

	"github.com/stacklok/toolhive/pkg/client"
	"github.com/stacklok/toolhive/pkg/config"
)

// MockManager for testing without Docker dependency
type MockManager struct {
	config     *config.Config
	configPath string
}

func (m *MockManager) ListClients() ([]client.Client, error) {
	clients := []client.Client{}
	for _, clientName := range m.config.Clients.RegisteredClients {
		clients = append(clients, client.Client{Name: client.MCPClient(clientName)})
	}
	return clients, nil
}

func (m *MockManager) RegisterClients(ctx context.Context, clients []client.Client) error {
	for _, client := range clients {
		// Check if client is already registered and skip.
		for _, registeredClient := range m.config.Clients.RegisteredClients {
			if registeredClient == string(client.Name) {
				continue
			}
		}

		// Add the client to the registered clients list
		m.config.Clients.RegisteredClients = append(m.config.Clients.RegisteredClients, string(client.Name))

		// Persist the updated config
		if err := m.SetConfig(m.config); err != nil {
			return err
		}
	}
	return nil
}

func (m *MockManager) UnregisterClients(ctx context.Context, clients []client.Client) error {
	for _, client := range clients {
		// Find and remove the client from registered clients list
		for i, registeredClient := range m.config.Clients.RegisteredClients {
			if registeredClient == string(client.Name) {
				// Remove client from slice
				m.config.Clients.RegisteredClients = append(m.config.Clients.RegisteredClients[:i], m.config.Clients.RegisteredClients[i+1:]...)
				break // Found and removed, no need to continue
			}
		}
		// Persist the updated config
		if err := m.SetConfig(m.config); err != nil {
			return err
		}
	}
	return nil
}

func (m *MockManager) GetConfig() (*config.Config, error) {
	// Ensure the directory exists
	configDir := filepath.Dir(m.configPath)
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create config directory: %w", err)
	}

	// Always reload from disk to get the latest state
	cfg, err := config.LoadOrCreateConfigWithPath(m.configPath)
	if err != nil {
		return nil, err
	}
	m.config = cfg
	cfgCopy := *cfg
	return &cfgCopy, nil
}

func (m *MockManager) SetConfig(cfg *config.Config) error {
	// Ensure the directory exists
	configDir := filepath.Dir(m.configPath)
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	// Update the in-memory config
	m.config = cfg

	// Persist to disk
	if m.configPath != "" {
		return cfg.SaveToPath(m.configPath)
	}
	return cfg.Save()
}

// resetViperState resets viper's state to prevent test interference
func resetViperState() {
	viper.Reset()
}

func NewMockManager(t *testing.T) *MockManager {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.yaml")
	// No need to set XDG_CONFIG_HOME, we always use --config
	// Create a default config
	cfg := config.Config{
		Secrets: config.Secrets{
			ProviderType:   "",
			SetupCompleted: false,
		},
		RegistryUrl:            "",
		AllowPrivateRegistryIp: false,
	}
	return &MockManager{
		config:     &cfg,
		configPath: configPath,
	}
}

func registerClientViaCLI(t *testing.T, configPath, clientName string) error {
	// Create a new root command for each test to ensure isolation
	rootCmd := NewRootCmd(false)

	// Capture output
	var out bytes.Buffer
	var errOut bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errOut)

	// Set args for client register command with config path
	args := []string{"--config", configPath, "client", "register", clientName}
	t.Logf("Executing command with args: %v", args)
	rootCmd.SetArgs(args)

	// Execute the command
	err := rootCmd.Execute()

	// Log output for debugging
	t.Logf("Command output: %s", out.String())
	t.Logf("Command error: %s", errOut.String())

	return err
}

func removeClientViaCLI(t *testing.T, configPath, clientName string) error {
	// Create a new root command for each test to ensure isolation
	rootCmd := NewRootCmd(false)

	// Capture output
	var out bytes.Buffer
	var errOut bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errOut)

	// Set args for client remove command with config path
	rootCmd.SetArgs([]string{"--config", configPath, "client", "remove", clientName})

	// Execute the command
	err := rootCmd.Execute()

	// Log output for debugging
	if err != nil {
		t.Logf("Command output: %s", out.String())
		t.Logf("Command error: %s", errOut.String())
	}

	return err
}

// NOTE: Cobra/pflag is not safe for parallel test execution.
// These tests must run sequentially.
func TestClientRegisterCmd(t *testing.T) {
	// Reset viper state to prevent test interference
	resetViperState()

	// Create test manager
	testManager := NewMockManager(t)

	t.Logf("Test manager config path: %s", testManager.configPath)

	// Register client via CLI using the same config path
	err := registerClientViaCLI(t, testManager.configPath, "vscode")
	assert.NoError(t, err)

	// Verify client was registered using the manager
	cfg, err := testManager.GetConfig()
	assert.NoError(t, err)
	t.Logf("Test manager config: %+v", cfg.Clients.RegisteredClients)
	assert.Contains(t, cfg.Clients.RegisteredClients, "vscode", "Client should be registered")
}

// NOTE: Cobra/pflag is not safe for parallel test execution.
// These tests must run sequentially.
func TestClientRemoveCmd(t *testing.T) {
	// Reset viper state to prevent test interference
	resetViperState()

	// Create test manager and ensure config file exists before CLI call
	testManager := NewMockManager(t)

	// Pre-populate config with a registered client and ensure file is created
	cfg := &config.Config{
		Secrets: config.Secrets{
			ProviderType:   "",
			SetupCompleted: false,
		},
		Clients: config.Clients{
			RegisteredClients: []string{"vscode"},
		},
	}
	err := testManager.SetConfig(cfg)
	if err != nil {
		t.Fatalf("Failed to pre-populate config: %v", err)
	}

	// Verify client is initially registered
	cfg, err = testManager.GetConfig()
	assert.NoError(t, err)
	assert.Contains(t, cfg.Clients.RegisteredClients, "vscode", "Client should be initially registered")

	// Remove client via CLI using the same config path
	err = removeClientViaCLI(t, testManager.configPath, "vscode")
	assert.NoError(t, err)

	// Verify client was removed using the manager
	cfg, err = testManager.GetConfig()
	assert.NoError(t, err)
	assert.NotContains(t, cfg.Clients.RegisteredClients, "vscode", "Client should be removed")
}

// NOTE: Cobra/pflag is not safe for parallel test execution.
// These tests must run sequentially.
func TestClientRegisterCmd_InvalidClient(t *testing.T) {
	// Reset viper state to prevent test interference
	resetViperState()

	// Create test manager
	testManager := NewMockManager(t)

	// Try to register invalid client via CLI using the same config path
	err := registerClientViaCLI(t, testManager.configPath, "not-a-client")
	assert.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "invalid client type"))

	// Verify no client was registered
	cfg, err := testManager.GetConfig()
	assert.NoError(t, err)
	assert.Empty(t, cfg.Clients.RegisteredClients, "No clients should be registered")
}

// NOTE: Cobra/pflag is not safe for parallel test execution.
// These tests must run sequentially.
func TestClientRemoveCmd_InvalidClient(t *testing.T) {
	// Reset viper state to prevent test interference
	resetViperState()

	// Create test manager
	testManager := NewMockManager(t)

	// Try to remove invalid client via CLI using the same config path
	err := removeClientViaCLI(t, testManager.configPath, "not-a-client")
	assert.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "invalid client type"))

	// Verify config is still empty
	cfg, err := testManager.GetConfig()
	assert.NoError(t, err)
	assert.Empty(t, cfg.Clients.RegisteredClients, "Config should remain empty")
}

// NOTE: Cobra/pflag is not safe for parallel test execution.
// These tests must run sequentially.
// TestClientRegisterAndRemove tests the full cycle of registering and removing a client
func TestClientRegisterAndRemove(t *testing.T) {
	// Reset viper state to prevent test interference
	resetViperState()

	// Create test manager and ensure config file exists before CLI call
	testManager := NewMockManager(t)

	// Pre-populate config with an empty config to ensure file exists
	emptyCfg := &config.Config{
		Secrets: config.Secrets{
			ProviderType:   "",
			SetupCompleted: false,
		},
	}
	err := testManager.SetConfig(emptyCfg)
	if err != nil {
		t.Fatalf("Failed to pre-populate config: %v", err)
	}

	// Register client using the same config path
	err = registerClientViaCLI(t, testManager.configPath, "vscode")
	assert.NoError(t, err)

	// Check if config file exists and log its contents
	if _, err := os.Stat(testManager.configPath); err == nil {
		content, _ := os.ReadFile(testManager.configPath)
		t.Logf("Config file contents after register: %s", string(content))
	} else {
		t.Logf("Config file does not exist after register: %v", err)
	}

	// Verify client is registered
	cfg, err := testManager.GetConfig()
	assert.NoError(t, err)
	t.Logf("Config after register: %+v", cfg.Clients.RegisteredClients)
	assert.Contains(t, cfg.Clients.RegisteredClients, "vscode", "Client should be registered")

	// Remove client using the same config path
	err = removeClientViaCLI(t, testManager.configPath, "vscode")
	assert.NoError(t, err)

	// Verify client is removed
	cfg, err = testManager.GetConfig()
	assert.NoError(t, err)
	assert.NotContains(t, cfg.Clients.RegisteredClients, "vscode", "Client should be removed")
}
