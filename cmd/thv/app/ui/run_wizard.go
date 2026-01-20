// Package ui provides terminal UI helpers for the ToolHive CLI.
package ui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	regtypes "github.com/stacklok/toolhive/pkg/registry/registry"
)

// Styles for the wizard
var (
	wizardDocStyle      = lipgloss.NewStyle().Margin(1, 2)
	wizardSelectedStyle = lipgloss.NewStyle().PaddingLeft(2).Foreground(lipgloss.Color("170"))
	wizardItemStyle     = lipgloss.NewStyle().PaddingLeft(2)
	wizardTitleStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("99"))
	wizardHelpStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	wizardPreviewStyle  = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(1, 2)
	wizardErrorStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
)

// WizardStep represents the current step in the wizard
type WizardStep int

const (
	// StepServerSource is the step for selecting the server source
	StepServerSource WizardStep = iota
	// StepServerSelection is the step for selecting/entering a server
	StepServerSelection
	// StepTransportSelection is the step for selecting the transport
	StepTransportSelection
	// StepAdvancedOptions is the step for advanced options
	StepAdvancedOptions
	// StepPreview is the step for previewing and confirming
	StepPreview
)

// ServerSourceType represents the type of server source
type ServerSourceType int

const (
	// SourceRegistry represents a registry server
	SourceRegistry ServerSourceType = iota
	// SourceImage represents a container image
	SourceImage
	// SourceProtocol represents a protocol scheme (uvx://, npx://, go://)
	SourceProtocol
	// SourceRemote represents a remote URL
	SourceRemote
)

// String returns the display string for a server source type
func (s ServerSourceType) String() string {
	switch s {
	case SourceRegistry:
		return "Registry server (browse MCP registry)"
	case SourceImage:
		return "Container image (e.g., ghcr.io/example/mcp-server)"
	case SourceProtocol:
		return "Protocol scheme (uvx://, npx://, go://)"
	case SourceRemote:
		return "Remote server (connect to URL)"
	default:
		return "Unknown"
	}
}

// WizardConfig holds the configuration collected by the wizard
type WizardConfig struct {
	ServerOrImage     string
	Name              string
	Transport         string
	Group             string
	PermissionProfile string
	EnvVars           []string
	Volumes           []string
	RemoteURL         string
	CmdArgs           []string
	IsRemote          bool
}

// GenerateCommand generates the thv run command string from the configuration
func (c *WizardConfig) GenerateCommand() string {
	var parts []string
	parts = append(parts, "thv run")

	if c.Name != "" {
		parts = append(parts, fmt.Sprintf("--name %s", c.Name))
	}

	if c.Transport != "" {
		parts = append(parts, fmt.Sprintf("--transport %s", c.Transport))
	}

	if c.Group != "" && c.Group != "default" {
		parts = append(parts, fmt.Sprintf("--group %s", c.Group))
	}

	if c.PermissionProfile != "" {
		parts = append(parts, fmt.Sprintf("--permission-profile %s", c.PermissionProfile))
	}

	for _, env := range c.EnvVars {
		parts = append(parts, fmt.Sprintf("-e %s", env))
	}

	for _, vol := range c.Volumes {
		parts = append(parts, fmt.Sprintf("-v %s", vol))
	}

	if c.IsRemote && c.RemoteURL != "" {
		parts = append(parts, c.RemoteURL)
	} else if c.ServerOrImage != "" {
		parts = append(parts, c.ServerOrImage)
	}

	if len(c.CmdArgs) > 0 {
		parts = append(parts, "--")
		parts = append(parts, c.CmdArgs...)
	}

	return strings.Join(parts, " ")
}

// RunWizardModel is the bubbletea model for the run wizard
type RunWizardModel struct {
	// Current step
	CurrentStep WizardStep

	// Step 1: Server source
	ServerSource ServerSourceType
	SourceCursor int

	// Step 2: Server selection
	RegistryServers []regtypes.ServerMetadata
	FilteredServers []regtypes.ServerMetadata
	ServerCursor    int
	SearchInput     textinput.Model
	TextInput       textinput.Model

	// Step 3: Transport
	Transport       string
	TransportCursor int

	// Step 4: Advanced options
	ShowAdvanced    bool
	AdvancedCursor  int
	NameInput       textinput.Model
	GroupInput      textinput.Model
	EnvInput        textinput.Model
	VolumeInput     textinput.Model
	CurrentAdvanced int // which advanced field is active
	EnvVars         []string
	Volumes         []string

	// Final state
	Config     *WizardConfig
	Quitting   bool
	Confirmed  bool
	Error      string
	EditMode   bool // True when in edit mode (text input active)
	InputFocus int  // Which input field has focus
}

// Available transports for container servers
var containerTransports = []string{"streamable-http", "sse", "stdio"}

// Available transports for remote servers
var remoteTransports = []string{"streamable-http", "sse"}

// NewRunWizardModel creates a new wizard model
func NewRunWizardModel(servers []regtypes.ServerMetadata) *RunWizardModel {
	// Sort servers by name
	sort.Slice(servers, func(i, j int) bool {
		return servers[i].GetName() < servers[j].GetName()
	})

	searchInput := textinput.New()
	searchInput.Placeholder = "Type to search..."
	searchInput.Width = 40

	textInput := textinput.New()
	textInput.Placeholder = "Enter value..."
	textInput.Width = 60

	nameInput := textinput.New()
	nameInput.Placeholder = "Server name (leave empty for auto)"
	nameInput.Width = 40

	groupInput := textinput.New()
	groupInput.Placeholder = "Group name"
	groupInput.Width = 40
	groupInput.SetValue("default")

	envInput := textinput.New()
	envInput.Placeholder = "KEY=VALUE"
	envInput.Width = 40

	volumeInput := textinput.New()
	volumeInput.Placeholder = "/host/path:/container/path[:ro]"
	volumeInput.Width = 50

	return &RunWizardModel{
		CurrentStep:     StepServerSource,
		RegistryServers: servers,
		FilteredServers: servers,
		SearchInput:     searchInput,
		TextInput:       textInput,
		NameInput:       nameInput,
		GroupInput:      groupInput,
		EnvInput:        envInput,
		VolumeInput:     volumeInput,
		Transport:       "streamable-http",
		Config:          &WizardConfig{},
	}
}

// Init implements tea.Model
func (*RunWizardModel) Init() tea.Cmd {
	return textinput.Blink
}

// Update implements tea.Model
func (m *RunWizardModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if keyMsg, ok := msg.(tea.KeyMsg); ok {
		return m.handleKeyPress(keyMsg)
	}

	// Update text inputs if they're active
	var cmd tea.Cmd
	switch m.CurrentStep {
	case StepServerSelection:
		if m.ServerSource == SourceRegistry {
			m.SearchInput, cmd = m.SearchInput.Update(msg)
			m.filterServers()
		} else if m.EditMode {
			m.TextInput, cmd = m.TextInput.Update(msg)
		}
	case StepAdvancedOptions:
		if m.EditMode {
			switch m.CurrentAdvanced {
			case 0:
				m.NameInput, cmd = m.NameInput.Update(msg)
			case 1:
				m.GroupInput, cmd = m.GroupInput.Update(msg)
			case 2:
				m.EnvInput, cmd = m.EnvInput.Update(msg)
			case 3:
				m.VolumeInput, cmd = m.VolumeInput.Update(msg)
			}
		}
	case StepServerSource, StepTransportSelection, StepPreview:
		// No text inputs active in these steps
	}

	return m, cmd
}

func (m *RunWizardModel) handleKeyPress(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	// Global keys
	switch key {
	case keyCtrlC:
		m.Quitting = true
		m.Confirmed = false
		return m, tea.Quit
	case keyQ:
		if !m.EditMode {
			m.Quitting = true
			m.Confirmed = false
			return m, tea.Quit
		}
	case keyEsc:
		if m.EditMode {
			m.EditMode = false
			m.SearchInput.Blur()
			m.TextInput.Blur()
			m.NameInput.Blur()
			m.GroupInput.Blur()
			m.EnvInput.Blur()
			m.VolumeInput.Blur()
			return m, nil
		}
		// Go back to previous step
		if m.CurrentStep > StepServerSource {
			m.CurrentStep--
			return m, nil
		}
	}

	// Handle step-specific keys
	switch m.CurrentStep {
	case StepServerSource:
		return m.handleServerSourceKeys(key)
	case StepServerSelection:
		return m.handleServerSelectionKeys(key, msg)
	case StepTransportSelection:
		return m.handleTransportSelectionKeys(key)
	case StepAdvancedOptions:
		return m.handleAdvancedOptionsKeys(key, msg)
	case StepPreview:
		return m.handlePreviewKeys(key)
	}

	return m, nil
}

func (m *RunWizardModel) handleServerSourceKeys(key string) (tea.Model, tea.Cmd) {
	switch key {
	case keyUp, keyK:
		if m.SourceCursor > 0 {
			m.SourceCursor--
			m.ServerSource = ServerSourceType(m.SourceCursor)
		}
	case keyDown, keyJ:
		if m.SourceCursor < 3 {
			m.SourceCursor++
			m.ServerSource = ServerSourceType(m.SourceCursor)
		}
	case keyEnter:
		m.CurrentStep = StepServerSelection
		if m.ServerSource == SourceRegistry {
			m.SearchInput.Focus()
			m.EditMode = true
		} else {
			m.TextInput.Focus()
			m.EditMode = true
			// Set appropriate placeholder based on source type
			switch m.ServerSource {
			case SourceImage:
				m.TextInput.Placeholder = "ghcr.io/example/mcp-server:latest"
			case SourceProtocol:
				m.TextInput.Placeholder = "uvx://package-name or npx://package-name"
			case SourceRemote:
				m.TextInput.Placeholder = "https://api.example.com/mcp"
			case SourceRegistry:
				// This case is handled above, but included for exhaustiveness
			}
		}
		return m, textinput.Blink
	}
	return m, nil
}

func (m *RunWizardModel) handleServerSelectionKeys(key string, msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.ServerSource == SourceRegistry {
		return m.handleRegistrySelectionKeys(key, msg)
	}
	return m.handleTextInputKeys(key, msg)
}

func (m *RunWizardModel) handleRegistrySelectionKeys(key string, msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key {
	case keyUp, keyK:
		if !m.SearchInput.Focused() && m.ServerCursor > 0 {
			m.ServerCursor--
		}
	case keyDown, keyJ:
		if !m.SearchInput.Focused() && m.ServerCursor < len(m.FilteredServers)-1 {
			m.ServerCursor++
		}
	case keyTab:
		if m.SearchInput.Focused() {
			m.SearchInput.Blur()
			m.EditMode = false
		} else {
			m.SearchInput.Focus()
			m.EditMode = true
			return m, textinput.Blink
		}
	case keyEnter:
		if len(m.FilteredServers) > 0 {
			server := m.FilteredServers[m.ServerCursor]
			m.Config.ServerOrImage = server.GetName()
			m.Config.Transport = server.GetTransport()
			m.Config.IsRemote = server.IsRemote()
			m.Transport = server.GetTransport()
			m.CurrentStep = StepTransportSelection
			m.SearchInput.Blur()
			m.EditMode = false
		}
	default:
		// Pass to search input if in edit mode
		if m.EditMode {
			var cmd tea.Cmd
			m.SearchInput, cmd = m.SearchInput.Update(msg)
			m.filterServers()
			return m, cmd
		}
	}
	return m, nil
}

func (m *RunWizardModel) handleTextInputKeys(key string, msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key {
	case keyEnter:
		value := m.TextInput.Value()
		if value == "" {
			m.Error = "Please enter a value"
			return m, nil
		}
		m.Error = ""

		switch m.ServerSource {
		case SourceImage:
			m.Config.ServerOrImage = value
			m.Config.IsRemote = false
		case SourceProtocol:
			m.Config.ServerOrImage = value
			m.Config.IsRemote = false
		case SourceRemote:
			m.Config.RemoteURL = value
			m.Config.ServerOrImage = value
			m.Config.IsRemote = true
		case SourceRegistry:
			// Registry selection is handled separately, not via text input
			m.Config.ServerOrImage = value
			m.Config.IsRemote = false
		}

		m.TextInput.Blur()
		m.EditMode = false
		m.CurrentStep = StepTransportSelection
	default:
		var cmd tea.Cmd
		m.TextInput, cmd = m.TextInput.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m *RunWizardModel) handleTransportSelectionKeys(key string) (tea.Model, tea.Cmd) {
	transports := m.getAvailableTransports()

	switch key {
	case keyUp, keyK:
		if m.TransportCursor > 0 {
			m.TransportCursor--
		}
	case keyDown, keyJ:
		if m.TransportCursor < len(transports)-1 {
			m.TransportCursor++
		}
	case keyEnter:
		m.Transport = transports[m.TransportCursor]
		m.Config.Transport = m.Transport
		m.CurrentStep = StepAdvancedOptions
	}
	return m, nil
}

// advancedFieldCount is the number of fields in advanced options (name, group, env vars, volumes, continue button)
const advancedFieldCount = 5

func (m *RunWizardModel) handleAdvancedOptionsKeys(key string, msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.EditMode {
		return m.handleAdvancedEditMode(key, msg)
	}
	return m.handleAdvancedNavigationMode(key)
}

func (m *RunWizardModel) handleAdvancedEditMode(key string, msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if key == keyEnter {
		m.saveAdvancedField()
		m.EditMode = false
		return m, nil
	}
	return m.updateAdvancedInput(msg)
}

func (m *RunWizardModel) saveAdvancedField() {
	switch m.CurrentAdvanced {
	case 0: // Name
		m.Config.Name = m.NameInput.Value()
		m.NameInput.Blur()
	case 1: // Group
		m.Config.Group = m.GroupInput.Value()
		m.GroupInput.Blur()
	case 2: // Env var
		if val := m.EnvInput.Value(); val != "" {
			m.EnvVars = append(m.EnvVars, val)
			m.Config.EnvVars = m.EnvVars
			m.EnvInput.SetValue("")
		}
		m.EnvInput.Blur()
	case 3: // Volume
		if val := m.VolumeInput.Value(); val != "" {
			m.Volumes = append(m.Volumes, val)
			m.Config.Volumes = m.Volumes
			m.VolumeInput.SetValue("")
		}
		m.VolumeInput.Blur()
	}
}

func (m *RunWizardModel) updateAdvancedInput(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	switch m.CurrentAdvanced {
	case 0:
		m.NameInput, cmd = m.NameInput.Update(msg)
	case 1:
		m.GroupInput, cmd = m.GroupInput.Update(msg)
	case 2:
		m.EnvInput, cmd = m.EnvInput.Update(msg)
	case 3:
		m.VolumeInput, cmd = m.VolumeInput.Update(msg)
	}
	return m, cmd
}

func (m *RunWizardModel) handleAdvancedNavigationMode(key string) (tea.Model, tea.Cmd) {
	switch key {
	case keyUp, keyK:
		if m.AdvancedCursor > 0 {
			m.AdvancedCursor--
		}
	case keyDown, keyJ:
		if m.AdvancedCursor < advancedFieldCount-1 {
			m.AdvancedCursor++
		}
	case keyEnter:
		return m.handleAdvancedEnter()
	case keyD:
		m.handleAdvancedDelete()
	}
	return m, nil
}

func (m *RunWizardModel) handleAdvancedEnter() (tea.Model, tea.Cmd) {
	if m.AdvancedCursor == advancedFieldCount-1 {
		// Continue button
		m.CurrentStep = StepPreview
		return m, nil
	}
	// Enter edit mode for the selected field
	m.CurrentAdvanced = m.AdvancedCursor
	m.EditMode = true
	return m.focusAdvancedInput()
}

func (m *RunWizardModel) focusAdvancedInput() (tea.Model, tea.Cmd) {
	switch m.CurrentAdvanced {
	case 0:
		m.NameInput.Focus()
	case 1:
		m.GroupInput.Focus()
	case 2:
		m.EnvInput.Focus()
	case 3:
		m.VolumeInput.Focus()
	}
	return m, textinput.Blink
}

func (m *RunWizardModel) handleAdvancedDelete() {
	// Delete last env var or volume
	if m.AdvancedCursor == 2 && len(m.EnvVars) > 0 {
		m.EnvVars = m.EnvVars[:len(m.EnvVars)-1]
		m.Config.EnvVars = m.EnvVars
	} else if m.AdvancedCursor == 3 && len(m.Volumes) > 0 {
		m.Volumes = m.Volumes[:len(m.Volumes)-1]
		m.Config.Volumes = m.Volumes
	}
}

func (m *RunWizardModel) handlePreviewKeys(key string) (tea.Model, tea.Cmd) {
	switch key {
	case keyEnter, keyY:
		m.Confirmed = true
		m.Quitting = true
		return m, tea.Quit
	case keyE:
		// Edit mode - go back to first step
		m.CurrentStep = StepServerSource
	case keyN:
		m.Confirmed = false
		m.Quitting = true
		return m, tea.Quit
	}
	return m, nil
}

func (m *RunWizardModel) filterServers() {
	query := strings.ToLower(m.SearchInput.Value())
	if query == "" {
		m.FilteredServers = m.RegistryServers
		return
	}

	filtered := make([]regtypes.ServerMetadata, 0)
	for _, server := range m.RegistryServers {
		name := strings.ToLower(server.GetName())
		desc := strings.ToLower(server.GetDescription())
		if strings.Contains(name, query) || strings.Contains(desc, query) {
			filtered = append(filtered, server)
		}
	}
	m.FilteredServers = filtered
	if m.ServerCursor >= len(m.FilteredServers) {
		m.ServerCursor = 0
	}
}

func (m *RunWizardModel) getAvailableTransports() []string {
	if m.Config.IsRemote {
		return remoteTransports
	}
	return containerTransports
}

// View implements tea.Model
func (m *RunWizardModel) View() string {
	if m.Quitting {
		return ""
	}

	var b strings.Builder

	switch m.CurrentStep {
	case StepServerSource:
		m.viewServerSource(&b)
	case StepServerSelection:
		m.viewServerSelection(&b)
	case StepTransportSelection:
		m.viewTransportSelection(&b)
	case StepAdvancedOptions:
		m.viewAdvancedOptions(&b)
	case StepPreview:
		m.viewPreview(&b)
	}

	return wizardDocStyle.Render(b.String())
}

func (m *RunWizardModel) viewServerSource(b *strings.Builder) {
	b.WriteString(wizardTitleStyle.Render("Step 1: Select server source") + "\n\n")

	sources := []ServerSourceType{SourceRegistry, SourceImage, SourceProtocol, SourceRemote}
	for i, source := range sources {
		cursor := "  "
		if m.SourceCursor == i {
			cursor = "> "
		}
		row := fmt.Sprintf("%s%s", cursor, source.String())
		if m.SourceCursor == i {
			b.WriteString(wizardSelectedStyle.Render(row) + "\n")
		} else {
			b.WriteString(wizardItemStyle.Render(row) + "\n")
		}
	}

	b.WriteString("\n" + wizardHelpStyle.Render("Use arrow keys or j/k to move, 'enter' to select, 'q' to quit"))
}

func (m *RunWizardModel) viewServerSelection(b *strings.Builder) {
	b.WriteString(wizardTitleStyle.Render("Step 2: Select or enter server") + "\n\n")

	if m.ServerSource == SourceRegistry {
		b.WriteString("Search: " + m.SearchInput.View() + "\n\n")

		if len(m.FilteredServers) == 0 {
			b.WriteString(wizardItemStyle.Render("No servers found matching your search.\n"))
		} else {
			// Show at most 10 servers
			start := 0
			end := len(m.FilteredServers)
			if end > 10 {
				// Implement scrolling
				if m.ServerCursor >= 5 && m.ServerCursor < len(m.FilteredServers)-5 {
					start = m.ServerCursor - 5
					end = m.ServerCursor + 5
				} else if m.ServerCursor >= len(m.FilteredServers)-5 {
					start = len(m.FilteredServers) - 10
				} else {
					end = 10
				}
			}

			for i := start; i < end; i++ {
				server := m.FilteredServers[i]
				cursor := "  "
				if m.ServerCursor == i {
					cursor = "> "
				}
				name := server.GetName()
				desc := server.GetDescription()
				if len(desc) > 50 {
					desc = desc[:47] + "..."
				}
				row := fmt.Sprintf("%s%s - %s", cursor, name, desc)
				if m.ServerCursor == i {
					b.WriteString(wizardSelectedStyle.Render(row) + "\n")
				} else {
					b.WriteString(wizardItemStyle.Render(row) + "\n")
				}
			}

			if len(m.FilteredServers) > 10 {
				fmt.Fprintf(b, "\n(showing %d-%d of %d)\n", start+1, end, len(m.FilteredServers))
			}
		}

		b.WriteString("\n" + wizardHelpStyle.Render("Tab to toggle search, arrow keys to select, 'enter' to confirm, 'esc' to go back"))
	} else {
		fmt.Fprintf(b, "Enter %s:\n\n", m.getSourceLabel())
		b.WriteString(m.TextInput.View() + "\n")

		if m.Error != "" {
			b.WriteString("\n" + wizardErrorStyle.Render(m.Error) + "\n")
		}

		b.WriteString("\n" + wizardHelpStyle.Render("Press 'enter' to confirm, 'esc' to go back"))
	}
}

func (m *RunWizardModel) viewTransportSelection(b *strings.Builder) {
	b.WriteString(wizardTitleStyle.Render("Step 3: Select transport") + "\n\n")

	transports := m.getAvailableTransports()
	for i, t := range transports {
		cursor := "  "
		if m.TransportCursor == i {
			cursor = "> "
		}
		desc := m.getTransportDescription(t)
		row := fmt.Sprintf("%s%s - %s", cursor, t, desc)
		if m.TransportCursor == i {
			b.WriteString(wizardSelectedStyle.Render(row) + "\n")
		} else {
			b.WriteString(wizardItemStyle.Render(row) + "\n")
		}
	}

	b.WriteString("\n" + wizardHelpStyle.Render("Use arrow keys to select, 'enter' to confirm, 'esc' to go back"))
}

func (m *RunWizardModel) viewAdvancedOptions(b *strings.Builder) {
	b.WriteString(wizardTitleStyle.Render("Step 4: Advanced options (optional)") + "\n\n")

	fields := []struct {
		label string
		value string
		input textinput.Model
	}{
		{"Name", m.Config.Name, m.NameInput},
		{"Group", m.Config.Group, m.GroupInput},
		{"Environment variables", strings.Join(m.EnvVars, ", "), m.EnvInput},
		{"Volumes", strings.Join(m.Volumes, ", "), m.VolumeInput},
	}

	for i, field := range fields {
		cursor := "  "
		if m.AdvancedCursor == i {
			cursor = "> "
		}

		if m.EditMode && m.CurrentAdvanced == i {
			fmt.Fprintf(b, "%s%s: %s\n", cursor, field.label, field.input.View())
		} else {
			displayValue := field.value
			if displayValue == "" {
				displayValue = "(not set)"
			}
			row := fmt.Sprintf("%s%s: %s", cursor, field.label, displayValue)
			if m.AdvancedCursor == i {
				b.WriteString(wizardSelectedStyle.Render(row) + "\n")
			} else {
				b.WriteString(wizardItemStyle.Render(row) + "\n")
			}
		}
	}

	// Continue button
	cursor := "  "
	if m.AdvancedCursor == 4 {
		cursor = "> "
	}
	continueRow := fmt.Sprintf("%s[Continue to preview]", cursor)
	if m.AdvancedCursor == 4 {
		b.WriteString("\n" + wizardSelectedStyle.Render(continueRow) + "\n")
	} else {
		b.WriteString("\n" + wizardItemStyle.Render(continueRow) + "\n")
	}

	help := "Use arrow keys to navigate, 'enter' to edit/select, 'esc' to go back"
	if m.AdvancedCursor == 2 || m.AdvancedCursor == 3 {
		help += ", 'd' to delete last item"
	}
	b.WriteString("\n" + wizardHelpStyle.Render(help))
}

func (m *RunWizardModel) viewPreview(b *strings.Builder) {
	b.WriteString(wizardTitleStyle.Render("Step 5: Preview and confirm") + "\n\n")

	command := m.Config.GenerateCommand()
	b.WriteString(wizardPreviewStyle.Render(command) + "\n\n")

	b.WriteString("Configuration summary:\n")
	fmt.Fprintf(b, "  Server: %s\n", m.Config.ServerOrImage)
	if m.Config.Name != "" {
		fmt.Fprintf(b, "  Name: %s\n", m.Config.Name)
	}
	fmt.Fprintf(b, "  Transport: %s\n", m.Config.Transport)
	if m.Config.Group != "" && m.Config.Group != "default" {
		fmt.Fprintf(b, "  Group: %s\n", m.Config.Group)
	}
	if len(m.Config.EnvVars) > 0 {
		fmt.Fprintf(b, "  Env vars: %d\n", len(m.Config.EnvVars))
	}
	if len(m.Config.Volumes) > 0 {
		fmt.Fprintf(b, "  Volumes: %d\n", len(m.Config.Volumes))
	}

	b.WriteString("\n" + wizardHelpStyle.Render("[Enter/y] Execute  [e] Edit  [n/q] Quit"))
}

func (m *RunWizardModel) getSourceLabel() string {
	switch m.ServerSource {
	case SourceImage:
		return "container image"
	case SourceProtocol:
		return "protocol scheme"
	case SourceRemote:
		return "remote URL"
	case SourceRegistry:
		return "registry server"
	}
	return "value"
}

func (*RunWizardModel) getTransportDescription(t string) string {
	switch t {
	case "streamable-http":
		return "HTTP-based streaming (recommended)"
	case "sse":
		return "Server-Sent Events (deprecated)"
	case "stdio":
		return "Standard input/output"
	default:
		return ""
	}
}

// GetConfig returns the final wizard configuration
func (m *RunWizardModel) GetConfig() *WizardConfig {
	return m.Config
}

// IsConfirmed returns whether the user confirmed the configuration
func (m *RunWizardModel) IsConfirmed() bool {
	return m.Confirmed
}

// RunWizard runs the interactive wizard and returns the configuration
func RunWizard(servers []regtypes.ServerMetadata) (*WizardConfig, bool, error) {
	model := NewRunWizardModel(servers)

	p := tea.NewProgram(model)
	finalModel, err := p.Run()
	if err != nil {
		return nil, false, err
	}

	m := finalModel.(*RunWizardModel)
	return m.GetConfig(), m.IsConfirmed(), nil
}
