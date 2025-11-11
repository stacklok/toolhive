package converters

import (
	"testing"

	"github.com/modelcontextprotocol/registry/pkg/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/registry/types"
)

// Test extracting environment variables from runtime arguments (-e flags)
func TestExtractEnvFromRuntimeArgs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		args     []model.Argument
		expected []*types.EnvVar
	}{
		{
			name: "single -e flag with variable reference",
			args: []model.Argument{
				{
					InputWithVariables: model.InputWithVariables{
						Input: model.Input{
							Value:       "GITHUB_PERSONAL_ACCESS_TOKEN={token}",
							Description: "Set an environment variable in the runtime",
							IsRequired:  true,
						},
						Variables: map[string]model.Input{
							"token": {
								IsRequired: true,
								IsSecret:   true,
								Format:     "string",
							},
						},
					},
					Type: model.ArgumentTypeNamed,
					Name: "-e",
				},
			},
			expected: []*types.EnvVar{
				{
					Name:        "GITHUB_PERSONAL_ACCESS_TOKEN",
					Description: "Set an environment variable in the runtime",
					Required:    true,
					Secret:      true,
				},
			},
		},
		{
			name: "multiple -e flags",
			args: []model.Argument{
				{
					InputWithVariables: model.InputWithVariables{
						Input: model.Input{
							Value:       "API_KEY={key}",
							Description: "API key",
							IsRequired:  true,
						},
						Variables: map[string]model.Input{
							"key": {
								IsRequired: true,
								IsSecret:   true,
							},
						},
					},
					Type: model.ArgumentTypeNamed,
					Name: "-e",
				},
				{
					InputWithVariables: model.InputWithVariables{
						Input: model.Input{
							Value:       "DEBUG=true",
							Description: "Enable debug mode",
							IsRequired:  false,
						},
					},
					Type: model.ArgumentTypeNamed,
					Name: "-e",
				},
			},
			expected: []*types.EnvVar{
				{
					Name:        "API_KEY",
					Description: "API key",
					Required:    true,
					Secret:      true,
				},
				{
					Name:        "DEBUG",
					Description: "Enable debug mode",
					Required:    false,
					Default:     "true",
				},
			},
		},
		{
			name: "--env flag variant",
			args: []model.Argument{
				{
					InputWithVariables: model.InputWithVariables{
						Input: model.Input{
							Value:       "TOKEN={token}",
							Description: "Auth token",
							IsRequired:  true,
						},
						Variables: map[string]model.Input{
							"token": {
								IsRequired: true,
								IsSecret:   true,
							},
						},
					},
					Type: model.ArgumentTypeNamed,
					Name: "--env",
				},
			},
			expected: []*types.EnvVar{
				{
					Name:        "TOKEN",
					Description: "Auth token",
					Required:    true,
					Secret:      true,
				},
			},
		},
		{
			name: "mixed with non-env arguments",
			args: []model.Argument{
				{
					InputWithVariables: model.InputWithVariables{
						Input: model.Input{
							Value: "run",
						},
					},
					Type: model.ArgumentTypePositional,
				},
				{
					InputWithVariables: model.InputWithVariables{
						Input: model.Input{
							Value: "true",
						},
					},
					Type: model.ArgumentTypeNamed,
					Name: "-i",
				},
				{
					InputWithVariables: model.InputWithVariables{
						Input: model.Input{
							Value:       "KEY=value",
							Description: "Some key",
						},
					},
					Type: model.ArgumentTypeNamed,
					Name: "-e",
				},
				{
					InputWithVariables: model.InputWithVariables{
						Input: model.Input{
							Value: "true",
						},
					},
					Type: model.ArgumentTypeNamed,
					Name: "--rm",
				},
			},
			expected: []*types.EnvVar{
				{
					Name:        "KEY",
					Description: "Some key",
					Default:     "value",
				},
			},
		},
		{
			name:     "no environment arguments",
			args:     []model.Argument{},
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := extractEnvFromRuntimeArgs(tt.args)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// Test parsing environment variable values
func TestParseEnvVarFromValue(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		value       string
		description string
		variables   map[string]model.Input
		expected    *types.EnvVar
	}{
		{
			name:        "static value",
			value:       "DEBUG=true",
			description: "Enable debug",
			variables:   nil,
			expected: &types.EnvVar{
				Name:        "DEBUG",
				Description: "Enable debug",
				Default:     "true",
			},
		},
		{
			name:        "variable reference with metadata",
			value:       "API_KEY={key}",
			description: "API key",
			variables: map[string]model.Input{
				"key": {
					IsRequired: true,
					IsSecret:   true,
					Default:    "default-key",
				},
			},
			expected: &types.EnvVar{
				Name:        "API_KEY",
				Description: "API key",
				Required:    true,
				Secret:      true,
				Default:     "default-key",
			},
		},
		{
			name:        "variable reference without metadata",
			value:       "TOKEN={token}",
			description: "Auth token",
			variables:   map[string]model.Input{},
			expected: &types.EnvVar{
				Name:        "TOKEN",
				Description: "Auth token",
			},
		},
		{
			name:        "empty value",
			value:       "",
			description: "Something",
			variables:   nil,
			expected:    nil,
		},
		{
			name:        "no equals sign",
			value:       "INVALID",
			description: "Invalid",
			variables:   nil,
			expected:    nil,
		},
		{
			name:        "complex value with equals",
			value:       "CONNECTION_STRING=host=localhost;port=5432",
			description: "Database connection",
			variables:   nil,
			expected: &types.EnvVar{
				Name:        "CONNECTION_STRING",
				Description: "Database connection",
				Default:     "host=localhost;port=5432",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := parseEnvVarFromValue(tt.value, tt.description, tt.variables)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// Test extracting environment variables from both sources
func TestExtractEnvironmentVariables(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		pkg      model.Package
		expected []*types.EnvVar
	}{
		{
			name: "from environmentVariables field only",
			pkg: model.Package{
				EnvironmentVariables: []model.KeyValueInput{
					{
						InputWithVariables: model.InputWithVariables{
							Input: model.Input{
								Description: "API key",
								IsRequired:  true,
								IsSecret:    true,
							},
						},
						Name: "API_KEY",
					},
				},
			},
			expected: []*types.EnvVar{
				{
					Name:        "API_KEY",
					Description: "API key",
					Required:    true,
					Secret:      true,
				},
			},
		},
		{
			name: "from runtimeArguments only",
			pkg: model.Package{
				RuntimeArguments: []model.Argument{
					{
						InputWithVariables: model.InputWithVariables{
							Input: model.Input{
								Value:       "TOKEN={token}",
								Description: "Auth token",
								IsRequired:  true,
							},
							Variables: map[string]model.Input{
								"token": {
									IsRequired: true,
									IsSecret:   true,
								},
							},
						},
						Type: model.ArgumentTypeNamed,
						Name: "-e",
					},
				},
			},
			expected: []*types.EnvVar{
				{
					Name:        "TOKEN",
					Description: "Auth token",
					Required:    true,
					Secret:      true,
				},
			},
		},
		{
			name: "from both sources combined",
			pkg: model.Package{
				EnvironmentVariables: []model.KeyValueInput{
					{
						InputWithVariables: model.InputWithVariables{
							Input: model.Input{
								Description: "Variable 1",
								IsRequired:  true,
							},
						},
						Name: "VAR1",
					},
				},
				RuntimeArguments: []model.Argument{
					{
						InputWithVariables: model.InputWithVariables{
							Input: model.Input{
								Value:       "VAR2={var2}",
								Description: "Variable 2",
								IsRequired:  true,
							},
							Variables: map[string]model.Input{
								"var2": {
									IsRequired: true,
									IsSecret:   true,
								},
							},
						},
						Type: model.ArgumentTypeNamed,
						Name: "-e",
					},
				},
			},
			expected: []*types.EnvVar{
				{
					Name:        "VAR1",
					Description: "Variable 1",
					Required:    true,
				},
				{
					Name:        "VAR2",
					Description: "Variable 2",
					Required:    true,
					Secret:      true,
				},
			},
		},
		{
			name:     "empty package",
			pkg:      model.Package{},
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := extractEnvironmentVariables(tt.pkg)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// Integration test with realistic GitHub MCP server data
func TestServerJSONToImageMetadata_GitHubServerEnvVars(t *testing.T) {
	t.Parallel()

	// Simulate the GitHub MCP server structure with -e flags
	serverJSON := createTestServerJSON()
	serverJSON.Name = "io.github.github/github-mcp-server"
	serverJSON.Packages[0].RuntimeArguments = []model.Argument{
		{
			InputWithVariables: model.InputWithVariables{
				Input: model.Input{
					Value:       "run",
					Description: "The runtime command to execute",
					IsRequired:  true,
				},
			},
			Type: model.ArgumentTypePositional,
		},
		{
			InputWithVariables: model.InputWithVariables{
				Input: model.Input{
					Value:       "true",
					Description: "Run container in interactive mode",
					IsRequired:  true,
					Format:      "boolean",
				},
			},
			Type: model.ArgumentTypeNamed,
			Name: "-i",
		},
		{
			InputWithVariables: model.InputWithVariables{
				Input: model.Input{
					Value:       "GITHUB_PERSONAL_ACCESS_TOKEN={token}",
					Description: "Set an environment variable in the runtime",
					IsRequired:  true,
				},
				Variables: map[string]model.Input{
					"token": {
						IsRequired: true,
						IsSecret:   true,
						Format:     "string",
					},
				},
			},
			Type: model.ArgumentTypeNamed,
			Name: "-e",
		},
	}

	result, err := ServerJSONToImageMetadata(serverJSON)
	require.NoError(t, err)
	require.NotNil(t, result)

	// Verify environment variable was extracted
	require.Len(t, result.EnvVars, 1)
	assert.Equal(t, "GITHUB_PERSONAL_ACCESS_TOKEN", result.EnvVars[0].Name)
	assert.Equal(t, "Set an environment variable in the runtime", result.EnvVars[0].Description)
	assert.True(t, result.EnvVars[0].Required)
	assert.True(t, result.EnvVars[0].Secret)
}
