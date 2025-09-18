package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/stacklok/toolhive/pkg/runner"
)

func TestLoadToolsOverride(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		jsonContent    string
		expectedResult *map[string]runner.ToolOverride
		expectError    bool
	}{
		{
			name: "valid tools override with name and description",
			jsonContent: `{
				"toolsOverride": {
					"original_tool": {
						"name": "renamed_tool",
						"description": "A new description for the tool"
					}
				}
			}`,
			expectedResult: &map[string]runner.ToolOverride{
				"original_tool": {
					Name:        "renamed_tool",
					Description: "A new description for the tool",
				},
			},
			expectError: false,
		},
		{
			name: "valid tools override with only name",
			jsonContent: `{
				"toolsOverride": {
					"tool1": {
						"name": "new_tool_name"
					}
				}
			}`,
			expectedResult: &map[string]runner.ToolOverride{
				"tool1": {
					Name: "new_tool_name",
				},
			},
			expectError: false,
		},
		{
			name: "valid tools override with only description",
			jsonContent: `{
				"toolsOverride": {
					"tool2": {
						"description": "Updated description only"
					}
				}
			}`,
			expectedResult: &map[string]runner.ToolOverride{
				"tool2": {
					Description: "Updated description only",
				},
			},
			expectError: false,
		},
		{
			name: "valid tools override with multiple tools",
			jsonContent: `{
				"toolsOverride": {
					"tool1": {
						"name": "renamed_tool1",
						"description": "Description for tool1"
					},
					"tool2": {
						"name": "renamed_tool2"
					},
					"tool3": {
						"description": "Description for tool3"
					}
				}
			}`,
			expectedResult: &map[string]runner.ToolOverride{
				"tool1": {
					Name:        "renamed_tool1",
					Description: "Description for tool1",
				},
				"tool2": {
					Name: "renamed_tool2",
				},
				"tool3": {
					Description: "Description for tool3",
				},
			},
			expectError: false,
		},
		{
			name: "valid empty tools override",
			jsonContent: `{
				"toolsOverride": {}
			}`,
			expectedResult: &map[string]runner.ToolOverride{},
			expectError:    false,
		},
		{
			name: "invalid JSON syntax",
			jsonContent: `{
				"toolsOverride": {
					"tool1": {
						"name": "invalid_json"
					}
				}
			`, // Missing closing brace
			expectedResult: nil,
			expectError:    true,
		},
		{
			name: "missing toolsOverride field",
			jsonContent: `{
				"otherField": "value"
			}`,
			expectedResult: nil,
			expectError:    true,
		},
		{
			name: "null toolsOverride field",
			jsonContent: `{
				"toolsOverride": null
			}`,
			expectedResult: nil,
			expectError:    true,
		},
		{
			name:           "empty file",
			jsonContent:    ``,
			expectedResult: nil,
			expectError:    true,
		},
		{
			name:           "non-JSON content",
			jsonContent:    `This is not JSON content`,
			expectedResult: nil,
			expectError:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Create a temporary file
			tmpFile, err := os.CreateTemp("", "tools_override_test_*.json")
			if err != nil {
				t.Fatalf("Failed to create temp file: %v", err)
			}
			defer os.Remove(tmpFile.Name())

			// Write test content to the file
			if tt.jsonContent != "" {
				_, err = tmpFile.WriteString(tt.jsonContent)
				if err != nil {
					t.Fatalf("Failed to write to temp file: %v", err)
				}
			}
			tmpFile.Close()

			// Test the LoadToolsOverride function
			result, err := LoadToolsOverride(tmpFile.Name())

			// Check error expectations
			if tt.expectError {
				assert.Error(t, err)
				assert.Nil(t, result)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, result)
				// Compare the results
				assert.Equal(t, tt.expectedResult, result)
			}
		})
	}
}

func TestLoadToolsOverride_FileNotFound(t *testing.T) {
	t.Parallel()

	// Test with non-existent file
	nonExistentFile := filepath.Join(os.TempDir(), "non_existent_file.json")

	result, err := LoadToolsOverride(nonExistentFile)

	if err == nil {
		t.Errorf("Expected error for non-existent file but got none")
	}

	if result != nil {
		t.Errorf("Expected nil result for non-existent file but got: %+v", result)
	}

	if !strings.Contains(err.Error(), "failed to open tools override file") {
		t.Errorf("Expected error to contain 'failed to open tools override file', but got: %v", err)
	}
}
