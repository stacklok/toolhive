package ui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	regtypes "github.com/stacklok/toolhive/pkg/registry/registry"
)

// mockServerMetadata implements regtypes.ServerMetadata for testing
type mockServerMetadata struct {
	name        string
	description string
	transport   string
	isRemote    bool
}

func (m *mockServerMetadata) GetName() string                 { return m.name }
func (m *mockServerMetadata) GetDescription() string          { return m.description }
func (*mockServerMetadata) GetTier() string                   { return "community" }
func (*mockServerMetadata) GetStatus() string                 { return "active" }
func (m *mockServerMetadata) GetTransport() string            { return m.transport }
func (*mockServerMetadata) GetTools() []string                { return nil }
func (*mockServerMetadata) GetMetadata() *regtypes.Metadata   { return nil }
func (*mockServerMetadata) GetRepositoryURL() string          { return "" }
func (*mockServerMetadata) GetTags() []string                 { return nil }
func (*mockServerMetadata) GetCustomMetadata() map[string]any { return nil }
func (m *mockServerMetadata) IsRemote() bool                  { return m.isRemote }
func (*mockServerMetadata) GetEnvVars() []*regtypes.EnvVar    { return nil }

func createTestServers() []regtypes.ServerMetadata {
	return []regtypes.ServerMetadata{
		&mockServerMetadata{name: "filesystem", description: "Local filesystem access", transport: "stdio", isRemote: false},
		&mockServerMetadata{name: "github", description: "GitHub integration", transport: "stdio", isRemote: false},
		&mockServerMetadata{name: "postgres", description: "PostgreSQL database", transport: "streamable-http", isRemote: false},
	}
}

func TestNewRunWizardModel(t *testing.T) {
	t.Parallel()
	servers := createTestServers()
	model := NewRunWizardModel(servers)

	assert.NotNil(t, model)
	assert.Equal(t, StepServerSource, model.CurrentStep)
	assert.Equal(t, SourceRegistry, model.ServerSource)
	assert.Len(t, model.RegistryServers, 3)
	assert.Len(t, model.FilteredServers, 3)
	assert.Equal(t, "streamable-http", model.Transport)
}

func TestWizardConfig_GenerateCommand(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		config   *WizardConfig
		expected string
	}{
		{
			name: "basic registry server",
			config: &WizardConfig{
				ServerOrImage: "filesystem",
				Transport:     "stdio",
			},
			expected: "thv run --transport stdio filesystem",
		},
		{
			name: "server with name and group",
			config: &WizardConfig{
				ServerOrImage: "github",
				Name:          "my-github",
				Transport:     "streamable-http",
				Group:         "production",
			},
			expected: "thv run --name my-github --transport streamable-http --group production github",
		},
		{
			name: "server with env vars and volumes",
			config: &WizardConfig{
				ServerOrImage: "postgres",
				Transport:     "streamable-http",
				EnvVars:       []string{"DB_HOST=localhost", "DB_PORT=5432"},
				Volumes:       []string{"/data:/var/lib/postgresql"},
			},
			expected: "thv run --transport streamable-http -e DB_HOST=localhost -e DB_PORT=5432 -v /data:/var/lib/postgresql postgres",
		},
		{
			name: "remote server",
			config: &WizardConfig{
				ServerOrImage: "https://api.example.com/mcp",
				RemoteURL:     "https://api.example.com/mcp",
				Transport:     "streamable-http",
				IsRemote:      true,
			},
			expected: "thv run --transport streamable-http https://api.example.com/mcp",
		},
		{
			name: "server with command args",
			config: &WizardConfig{
				ServerOrImage: "github",
				Transport:     "stdio",
				CmdArgs:       []string{"--toolsets", "repos"},
			},
			expected: "thv run --transport stdio github -- --toolsets repos",
		},
		{
			name: "default group not included",
			config: &WizardConfig{
				ServerOrImage: "filesystem",
				Transport:     "stdio",
				Group:         "default",
			},
			expected: "thv run --transport stdio filesystem",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := tt.config.GenerateCommand()
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestServerSourceType_String(t *testing.T) {
	t.Parallel()
	tests := []struct {
		source   ServerSourceType
		expected string
	}{
		{SourceRegistry, "Registry server (browse MCP registry)"},
		{SourceImage, "Container image (e.g., ghcr.io/example/mcp-server)"},
		{SourceProtocol, "Protocol scheme (uvx://, npx://, go://)"},
		{SourceRemote, "Remote server (connect to URL)"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.expected, tt.source.String())
		})
	}
}

func TestWizardNavigation_ServerSource(t *testing.T) {
	t.Parallel()
	m := NewRunWizardModel(createTestServers())

	// Test down navigation
	newModel, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = newModel.(*RunWizardModel)
	assert.Equal(t, 1, m.SourceCursor)
	assert.Equal(t, SourceImage, m.ServerSource)

	// Test up navigation
	newModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = newModel.(*RunWizardModel)
	assert.Equal(t, 0, m.SourceCursor)
	assert.Equal(t, SourceRegistry, m.ServerSource)

	// Test vim keys
	newModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	m = newModel.(*RunWizardModel)
	assert.Equal(t, 1, m.SourceCursor)

	newModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	m = newModel.(*RunWizardModel)
	assert.Equal(t, 0, m.SourceCursor)
}

func TestWizardNavigation_EnterServerSelection(t *testing.T) {
	t.Parallel()
	m := NewRunWizardModel(createTestServers())

	// Press enter to go to server selection
	newModel, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = newModel.(*RunWizardModel)

	assert.Equal(t, StepServerSelection, m.CurrentStep)
	assert.True(t, m.SearchInput.Focused())
	assert.True(t, m.EditMode)
}

func TestWizardNavigation_TransportSelection(t *testing.T) {
	t.Parallel()
	m := NewRunWizardModel(createTestServers())

	// Set up to transport selection step
	m.CurrentStep = StepTransportSelection
	m.Config.IsRemote = false

	// Test transport navigation
	newModel, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = newModel.(*RunWizardModel)
	assert.Equal(t, 1, m.TransportCursor)

	newModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = newModel.(*RunWizardModel)
	assert.Equal(t, 0, m.TransportCursor)
}

func TestWizardQuit(t *testing.T) {
	t.Parallel()
	m := NewRunWizardModel(createTestServers())

	// Test q to quit
	newModel, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	m = newModel.(*RunWizardModel)

	assert.True(t, m.Quitting)
	assert.False(t, m.Confirmed)
}

func TestWizardCtrlCQuit(t *testing.T) {
	t.Parallel()
	m := NewRunWizardModel(createTestServers())

	// Test ctrl+c to quit
	newModel, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	m = newModel.(*RunWizardModel)

	assert.True(t, m.Quitting)
	assert.False(t, m.Confirmed)
}

func TestWizardEscGoBack(t *testing.T) {
	t.Parallel()
	m := NewRunWizardModel(createTestServers())

	// Move to server selection step
	m.CurrentStep = StepServerSelection

	// Press esc to go back
	newModel, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = newModel.(*RunWizardModel)

	assert.Equal(t, StepServerSource, m.CurrentStep)
}

func TestWizardServerFiltering(t *testing.T) {
	t.Parallel()
	m := NewRunWizardModel(createTestServers())

	// Test filtering
	m.SearchInput.SetValue("git")
	m.filterServers()

	assert.Len(t, m.FilteredServers, 1)
	assert.Equal(t, "github", m.FilteredServers[0].GetName())
}

func TestWizardServerFilteringNoMatch(t *testing.T) {
	t.Parallel()
	m := NewRunWizardModel(createTestServers())

	// Test filtering with no match
	m.SearchInput.SetValue("nonexistent")
	m.filterServers()

	assert.Empty(t, m.FilteredServers)
}

func TestWizardServerFilteringEmpty(t *testing.T) {
	t.Parallel()
	m := NewRunWizardModel(createTestServers())

	// Test filtering with empty query shows all
	m.SearchInput.SetValue("")
	m.filterServers()

	assert.Len(t, m.FilteredServers, 3)
}

func TestWizardGetAvailableTransports(t *testing.T) {
	t.Parallel()
	m := NewRunWizardModel(createTestServers())

	// Container server transports
	m.Config.IsRemote = false
	transports := m.getAvailableTransports()
	assert.Equal(t, []string{"streamable-http", "sse", "stdio"}, transports)

	// Remote server transports
	m.Config.IsRemote = true
	transports = m.getAvailableTransports()
	assert.Equal(t, []string{"streamable-http", "sse"}, transports)
}

func TestWizardView(t *testing.T) {
	t.Parallel()
	m := NewRunWizardModel(createTestServers())

	// Test view for each step
	steps := []WizardStep{
		StepServerSource,
		StepServerSelection,
		StepTransportSelection,
		StepAdvancedOptions,
		StepPreview,
	}

	for _, step := range steps {
		m.CurrentStep = step
		view := m.View()
		assert.NotEmpty(t, view, "View should not be empty for step %d", step)
	}
}

func TestWizardViewQuitting(t *testing.T) {
	t.Parallel()
	m := NewRunWizardModel(createTestServers())

	m.Quitting = true
	view := m.View()
	assert.Empty(t, view, "View should be empty when quitting")
}

func TestWizardInit(t *testing.T) {
	t.Parallel()
	m := NewRunWizardModel(createTestServers())
	cmd := m.Init()
	require.NotNil(t, cmd)
}

func TestWizardPreviewConfirm(t *testing.T) {
	t.Parallel()
	m := NewRunWizardModel(createTestServers())

	// Set up for preview step
	m.CurrentStep = StepPreview
	m.Config.ServerOrImage = "filesystem"
	m.Config.Transport = "stdio"

	// Press enter to confirm
	newModel, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = newModel.(*RunWizardModel)

	assert.True(t, m.Confirmed)
	assert.True(t, m.Quitting)
}

func TestWizardPreviewEdit(t *testing.T) {
	t.Parallel()
	m := NewRunWizardModel(createTestServers())

	// Set up for preview step
	m.CurrentStep = StepPreview

	// Press 'e' to edit
	newModel, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	m = newModel.(*RunWizardModel)

	assert.Equal(t, StepServerSource, m.CurrentStep)
	assert.False(t, m.Confirmed)
}

func TestWizardPreviewCancel(t *testing.T) {
	t.Parallel()
	m := NewRunWizardModel(createTestServers())

	// Set up for preview step
	m.CurrentStep = StepPreview

	// Press 'n' to cancel
	newModel, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	m = newModel.(*RunWizardModel)

	assert.False(t, m.Confirmed)
	assert.True(t, m.Quitting)
}

func TestWizardAdvancedOptionsNavigation(t *testing.T) {
	t.Parallel()
	m := NewRunWizardModel(createTestServers())

	// Set up for advanced options step
	m.CurrentStep = StepAdvancedOptions

	// Navigate down through fields
	newModel, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = newModel.(*RunWizardModel)
	assert.Equal(t, 1, m.AdvancedCursor)

	newModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = newModel.(*RunWizardModel)
	assert.Equal(t, 2, m.AdvancedCursor)

	newModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = newModel.(*RunWizardModel)
	assert.Equal(t, 3, m.AdvancedCursor)

	newModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = newModel.(*RunWizardModel)
	assert.Equal(t, 4, m.AdvancedCursor) // Continue button

	// Navigate up
	newModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = newModel.(*RunWizardModel)
	assert.Equal(t, 3, m.AdvancedCursor)
}

func TestWizardGetters(t *testing.T) {
	t.Parallel()
	m := NewRunWizardModel(createTestServers())

	m.Config.ServerOrImage = "test-server"
	m.Confirmed = true

	assert.Equal(t, m.Config, m.GetConfig())
	assert.True(t, m.IsConfirmed())
}

func TestWizardTransportDescription(t *testing.T) {
	t.Parallel()
	m := NewRunWizardModel(createTestServers())

	assert.Equal(t, "HTTP-based streaming (recommended)", m.getTransportDescription("streamable-http"))
	assert.Equal(t, "Server-Sent Events (deprecated)", m.getTransportDescription("sse"))
	assert.Equal(t, "Standard input/output", m.getTransportDescription("stdio"))
	assert.Equal(t, "", m.getTransportDescription("unknown"))
}

func TestWizardSourceLabel(t *testing.T) {
	t.Parallel()
	m := NewRunWizardModel(createTestServers())

	m.ServerSource = SourceImage
	assert.Equal(t, "container image", m.getSourceLabel())

	m.ServerSource = SourceProtocol
	assert.Equal(t, "protocol scheme", m.getSourceLabel())

	m.ServerSource = SourceRemote
	assert.Equal(t, "remote URL", m.getSourceLabel())

	m.ServerSource = SourceRegistry
	assert.Equal(t, "registry server", m.getSourceLabel())
}

func TestWizardEmptyServers(t *testing.T) {
	t.Parallel()
	m := NewRunWizardModel(nil)

	assert.NotNil(t, m)
	assert.Empty(t, m.RegistryServers)
	assert.Empty(t, m.FilteredServers)
}
