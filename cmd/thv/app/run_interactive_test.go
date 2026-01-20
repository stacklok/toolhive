package app

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/stacklok/toolhive/cmd/thv/app/ui"
	"github.com/stacklok/toolhive/pkg/runner/retriever"
	"github.com/stacklok/toolhive/pkg/transport"
)

func TestWizardConfigToRunFlags(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		config   *ui.WizardConfig
		validate func(t *testing.T, flags *RunFlags)
	}{
		{
			name: "sets default host values",
			config: &ui.WizardConfig{
				ServerOrImage: "filesystem",
				Transport:     "stdio",
			},
			validate: func(t *testing.T, flags *RunFlags) {
				t.Helper()
				assert.Equal(t, transport.LocalhostIPv4, flags.Host, "Host should be set to default")
				assert.Equal(t, transport.LocalhostIPv4, flags.TargetHost, "TargetHost should be set to default")
			},
		},
		{
			name: "sets default group when empty",
			config: &ui.WizardConfig{
				ServerOrImage: "filesystem",
				Transport:     "stdio",
				Group:         "",
			},
			validate: func(t *testing.T, flags *RunFlags) {
				t.Helper()
				assert.Equal(t, "default", flags.Group)
			},
		},
		{
			name: "preserves custom group",
			config: &ui.WizardConfig{
				ServerOrImage: "filesystem",
				Transport:     "stdio",
				Group:         "production",
			},
			validate: func(t *testing.T, flags *RunFlags) {
				t.Helper()
				assert.Equal(t, "production", flags.Group)
			},
		},
		{
			name: "sets remote URL for remote servers",
			config: &ui.WizardConfig{
				ServerOrImage: "https://api.example.com/mcp",
				RemoteURL:     "https://api.example.com/mcp",
				Transport:     "streamable-http",
				IsRemote:      true,
			},
			validate: func(t *testing.T, flags *RunFlags) {
				t.Helper()
				assert.Equal(t, "https://api.example.com/mcp", flags.RemoteURL)
			},
		},
		{
			name: "sets name and transport",
			config: &ui.WizardConfig{
				ServerOrImage: "github",
				Name:          "my-github",
				Transport:     "streamable-http",
			},
			validate: func(t *testing.T, flags *RunFlags) {
				t.Helper()
				assert.Equal(t, "my-github", flags.Name)
				assert.Equal(t, "streamable-http", flags.Transport)
			},
		},
		{
			name: "sets env vars and volumes",
			config: &ui.WizardConfig{
				ServerOrImage: "postgres",
				Transport:     "streamable-http",
				EnvVars:       []string{"DB_HOST=localhost", "DB_PORT=5432"},
				Volumes:       []string{"/data:/var/lib/postgresql"},
			},
			validate: func(t *testing.T, flags *RunFlags) {
				t.Helper()
				assert.Equal(t, []string{"DB_HOST=localhost", "DB_PORT=5432"}, flags.Env)
				assert.Equal(t, []string{"/data:/var/lib/postgresql"}, flags.Volumes)
			},
		},
		{
			name: "enables ignore globally by default",
			config: &ui.WizardConfig{
				ServerOrImage: "filesystem",
				Transport:     "stdio",
			},
			validate: func(t *testing.T, flags *RunFlags) {
				t.Helper()
				assert.True(t, flags.IgnoreGlobally)
			},
		},
		{
			name: "sets default image verification mode",
			config: &ui.WizardConfig{
				ServerOrImage: "filesystem",
				Transport:     "stdio",
			},
			validate: func(t *testing.T, flags *RunFlags) {
				t.Helper()
				assert.Equal(t, retriever.VerifyImageWarn, flags.VerifyImage)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			flags := wizardConfigToRunFlags(tt.config)
			tt.validate(t, flags)
		})
	}
}

func TestWizardConfigToRunFlags_HostValidation(t *testing.T) {
	t.Parallel()

	// This test ensures the host value from wizard config can be validated
	config := &ui.WizardConfig{
		ServerOrImage: "filesystem",
		Transport:     "stdio",
	}

	flags := wizardConfigToRunFlags(config)

	// The host should be valid for ValidateAndNormaliseHostFlag
	validatedHost, err := ValidateAndNormaliseHostFlag(flags.Host)
	assert.NoError(t, err, "Host from wizard config should be valid")
	assert.Equal(t, transport.LocalhostIPv4, validatedHost)
}
