package labels

import (
	"testing"
)

func TestAddStandardLabels(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name              string
		containerName     string
		containerBaseName string
		transportType     string
		port              int
		expected          map[string]string
	}{
		{
			name:              "Standard labels",
			containerName:     "test-container",
			containerBaseName: "test-base",
			transportType:     "http",
			port:              8080,
			expected: map[string]string{
				LabelToolHive:  "true",
				LabelName:      "test-container",
				LabelBaseName:  "test-base",
				LabelTransport: "http",
				LabelPort:      "8080",
				LabelToolType:  "mcp",
			},
		},
		{
			name:              "Different port",
			containerName:     "another-container",
			containerBaseName: "another-base",
			transportType:     "https",
			port:              9090,
			expected: map[string]string{
				LabelToolHive:  "true",
				LabelName:      "another-container",
				LabelBaseName:  "another-base",
				LabelTransport: "https",
				LabelPort:      "9090",
				LabelToolType:  "mcp",
			},
		},
		{
			name:              "With group",
			containerName:     "group-container",
			containerBaseName: "group-base",
			transportType:     "sse",
			port:              7070,
			expected: map[string]string{
				LabelToolHive:  "true",
				LabelName:      "group-container",
				LabelBaseName:  "group-base",
				LabelTransport: "sse",
				LabelPort:      "7070",
				LabelToolType:  "mcp",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			labels := make(map[string]string)
			AddStandardLabels(labels, tc.containerName, tc.containerBaseName, tc.transportType, tc.port)

			// Verify all expected labels are present with correct values
			for key, expectedValue := range tc.expected {
				actualValue, exists := labels[key]
				if !exists {
					t.Errorf("Expected label %s to exist, but it doesn't", key)
				}
				if actualValue != expectedValue {
					t.Errorf("Expected label %s to be %s, but got %s", key, expectedValue, actualValue)
				}
			}

			// Verify no unexpected labels are present
			if len(labels) != len(tc.expected) {
				t.Errorf("Expected %d labels, but got %d", len(tc.expected), len(labels))
			}
		})
	}
}

func TestFormatToolHiveFilter(t *testing.T) {
	t.Parallel()
	expected := "toolhive=true"
	result := FormatToolHiveFilter()
	if result != expected {
		t.Errorf("Expected filter to be %s, but got %s", expected, result)
	}
}

func TestIsToolHiveContainer(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		labels   map[string]string
		expected bool
	}{
		{
			name: "Valid ToolHive container",
			labels: map[string]string{
				LabelToolHive: "true",
			},
			expected: true,
		},
		{
			name: "Valid ToolHive container with uppercase TRUE",
			labels: map[string]string{
				LabelToolHive: "TRUE",
			},
			expected: true,
		},
		{
			name: "Valid ToolHive container with mixed case TrUe",
			labels: map[string]string{
				LabelToolHive: "TrUe",
			},
			expected: true,
		},
		{
			name: "Not a ToolHive container - false value",
			labels: map[string]string{
				LabelToolHive: "false",
			},
			expected: false,
		},
		{
			name: "Not a ToolHive container - other value",
			labels: map[string]string{
				LabelToolHive: "yes",
			},
			expected: false,
		},
		{
			name:     "Not a ToolHive container - empty labels",
			labels:   map[string]string{},
			expected: false,
		},
		{
			name: "Not a ToolHive container - label missing",
			labels: map[string]string{
				"some-other-label": "value",
			},
			expected: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result := IsToolHiveContainer(tc.labels)
			if result != tc.expected {
				t.Errorf("Expected IsToolHiveContainer to return %v, but got %v", tc.expected, result)
			}
		})
	}
}

func TestGetContainerName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		labels   map[string]string
		expected string
	}{
		{
			name: "Container name exists",
			labels: map[string]string{
				LabelName: "test-container",
			},
			expected: "test-container",
		},
		{
			name:     "Container name doesn't exist",
			labels:   map[string]string{},
			expected: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result := GetContainerName(tc.labels)
			if result != tc.expected {
				t.Errorf("Expected container name to be %s, but got %s", tc.expected, result)
			}
		})
	}
}

func TestGetContainerBaseName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		labels   map[string]string
		expected string
	}{
		{
			name: "Container base name exists",
			labels: map[string]string{
				LabelBaseName: "test-base",
			},
			expected: "test-base",
		},
		{
			name:     "Container base name doesn't exist",
			labels:   map[string]string{},
			expected: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result := GetContainerBaseName(tc.labels)
			if result != tc.expected {
				t.Errorf("Expected container base name to be %s, but got %s", tc.expected, result)
			}
		})
	}
}

func TestGetTransportType(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		labels   map[string]string
		expected string
	}{
		{
			name: "Transport type exists",
			labels: map[string]string{
				LabelTransport: "http",
			},
			expected: "http",
		},
		{
			name:     "Transport type doesn't exist",
			labels:   map[string]string{},
			expected: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result := GetTransportType(tc.labels)
			if result != tc.expected {
				t.Errorf("Expected transport type to be %s, but got %s", tc.expected, result)
			}
		})
	}
}

func TestGetPort(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		labels      map[string]string
		expected    int
		expectError bool
		errorMsg    string
	}{
		{
			name: "Valid port",
			labels: map[string]string{
				LabelPort: "8080",
			},
			expected:    8080,
			expectError: false,
		},
		{
			name:        "Port label missing",
			labels:      map[string]string{},
			expected:    0,
			expectError: true,
			errorMsg:    "port label not found",
		},
		{
			name: "Invalid port - not a number",
			labels: map[string]string{
				LabelPort: "not-a-number",
			},
			expected:    0,
			expectError: true,
			errorMsg:    "invalid port: not-a-number",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result, err := GetPort(tc.labels)

			// Check error
			if tc.expectError {
				if err == nil {
					t.Errorf("Expected error but got nil")
				} else if err.Error() != tc.errorMsg {
					t.Errorf("Expected error message '%s', but got '%s'", tc.errorMsg, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("Expected no error but got: %v", err)
				}
			}

			// Check result
			if result != tc.expected {
				t.Errorf("Expected port to be %d, but got %d", tc.expected, result)
			}
		})
	}
}

func TestHasNetworkIsolation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		labels   map[string]string
		expected bool
	}{
		{
			name: "Network isolation enabled",
			labels: map[string]string{
				LabelNetworkIsolation: "true",
			},
			expected: true,
		},
		{
			name: "Network isolation disabled",
			labels: map[string]string{
				LabelNetworkIsolation: "false",
			},
			expected: false,
		},
		{
			name:     "Legacy workload without label",
			labels:   map[string]string{},
			expected: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result := HasNetworkIsolation(tc.labels)
			if result != tc.expected {
				t.Errorf("Expected HasNetworkIsolation to be %t, but got %t", tc.expected, result)
			}
		})
	}
}

func TestIsStandardToolHiveLabel(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		key      string
		expected bool
	}{
		{
			name:     "Standard ToolHive label",
			key:      LabelToolHive,
			expected: true,
		},
		{
			name:     "Standard name label",
			key:      LabelName,
			expected: true,
		},
		{
			name:     "Standard base name label",
			key:      LabelBaseName,
			expected: true,
		},
		{
			name:     "Standard transport label",
			key:      LabelTransport,
			expected: true,
		},
		{
			name:     "Standard port label",
			key:      LabelPort,
			expected: true,
		},
		{
			name:     "Standard tool type label",
			key:      LabelToolType,
			expected: true,
		},
		{
			name:     "Standard network isolation label",
			key:      LabelNetworkIsolation,
			expected: true,
		},
		{
			name:     "User-defined label",
			key:      "user-label",
			expected: false,
		},
		{
			name:     "Custom application label",
			key:      "app.kubernetes.io/name",
			expected: false,
		},
		{
			name:     "Empty key",
			key:      "",
			expected: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result := IsStandardToolHiveLabel(tc.key)
			if result != tc.expected {
				t.Errorf("Expected IsStandardToolHiveLabel(%s) to return %v, but got %v", tc.key, tc.expected, result)
			}
		})
	}
}

func TestParseLabel(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		label       string
		expectedKey string
		expectedVal string
		expectError bool
		errorMsg    string
	}{
		{
			name:        "Valid label",
			label:       "key=value",
			expectedKey: "key",
			expectedVal: "value",
			expectError: false,
		},
		{
			name:        "Label with spaces",
			label:       " key = value ",
			expectedKey: "key",
			expectedVal: "value",
			expectError: false,
		},
		{
			name:        "Label with empty value",
			label:       "key=",
			expectedKey: "key",
			expectedVal: "",
			expectError: false,
		},
		{
			name:        "Label with equals in value - should fail validation",
			label:       "key=value=with=equals",
			expectedKey: "",
			expectedVal: "",
			expectError: true,
			errorMsg:    "invalid label value: value must consist of alphanumeric characters, '-', '_' or '.', and must start and end with an alphanumeric character",
		},
		{
			name:        "Complex key with prefix",
			label:       "app.kubernetes.io/name=myapp",
			expectedKey: "app.kubernetes.io/name",
			expectedVal: "myapp",
			expectError: false,
		},
		{
			name:        "Missing equals sign",
			label:       "keyvalue",
			expectedKey: "",
			expectedVal: "",
			expectError: true,
			errorMsg:    "invalid label format, expected key=value",
		},
		{
			name:        "Empty key",
			label:       "=value",
			expectedKey: "",
			expectedVal: "",
			expectError: true,
			errorMsg:    "label key cannot be empty",
		},
		{
			name:        "Only spaces as key",
			label:       "   =value",
			expectedKey: "",
			expectedVal: "",
			expectError: true,
			errorMsg:    "label key cannot be empty",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			key, value, err := ParseLabel(tc.label)

			// Check error
			if tc.expectError {
				if err == nil {
					t.Errorf("Expected error but got nil")
				} else if err.Error() != tc.errorMsg {
					t.Errorf("Expected error message '%s', but got '%s'", tc.errorMsg, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("Expected no error but got: %v", err)
				}
			}

			// Check results
			if key != tc.expectedKey {
				t.Errorf("Expected key to be '%s', but got '%s'", tc.expectedKey, key)
			}
			if value != tc.expectedVal {
				t.Errorf("Expected value to be '%s', but got '%s'", tc.expectedVal, value)
			}
		})
	}
}

func TestParseLabelValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		label       string
		expectedKey string
		expectedVal string
		expectError bool
		errorMsg    string
	}{
		{
			name:        "Valid simple label",
			label:       "app=myapp",
			expectedKey: "app",
			expectedVal: "myapp",
			expectError: false,
		},
		{
			name:        "Valid label with prefix",
			label:       "app.kubernetes.io/name=myapp",
			expectedKey: "app.kubernetes.io/name",
			expectedVal: "myapp",
			expectError: false,
		},
		{
			name:        "Valid label with hyphens and underscores",
			label:       "my-app_version=1.0.0",
			expectedKey: "my-app_version",
			expectedVal: "1.0.0",
			expectError: false,
		},
		{
			name:        "Valid label with empty value",
			label:       "environment=",
			expectedKey: "environment",
			expectedVal: "",
			expectError: false,
		},
		{
			name:        "Invalid format - missing equals",
			label:       "keyvalue",
			expectedKey: "",
			expectedVal: "",
			expectError: true,
			errorMsg:    "invalid label format, expected key=value",
		},
		{
			name:        "Invalid key - too long",
			label:       "a" + string(make([]byte, 254)) + "=value",
			expectedKey: "",
			expectedVal: "",
			expectError: true,
			errorMsg:    "invalid label key: key cannot be longer than 253 characters",
		},
		{
			name:        "Invalid key - starts with non-alphanumeric",
			label:       "-invalid=value",
			expectedKey: "",
			expectedVal: "",
			expectError: true,
			errorMsg:    "invalid label key: name part must consist of alphanumeric characters, '-', '_' or '.', and must start and end with an alphanumeric character",
		},
		{
			name:        "Invalid key - ends with non-alphanumeric",
			label:       "invalid-=value",
			expectedKey: "",
			expectedVal: "",
			expectError: true,
			errorMsg:    "invalid label key: name part must consist of alphanumeric characters, '-', '_' or '.', and must start and end with an alphanumeric character",
		},
		{
			name:        "Invalid key - contains invalid characters",
			label:       "invalid@key=value",
			expectedKey: "",
			expectedVal: "",
			expectError: true,
			errorMsg:    "invalid label key: name part must consist of alphanumeric characters, '-', '_' or '.', and must start and end with an alphanumeric character",
		},
		{
			name:        "Invalid value - too long",
			label:       "key=" + string(make([]byte, 64)),
			expectedKey: "",
			expectedVal: "",
			expectError: true,
			errorMsg:    "invalid label value: value cannot be longer than 63 characters",
		},
		{
			name:        "Invalid value - starts with non-alphanumeric",
			label:       "key=-invalid",
			expectedKey: "",
			expectedVal: "",
			expectError: true,
			errorMsg:    "invalid label value: value must consist of alphanumeric characters, '-', '_' or '.', and must start and end with an alphanumeric character",
		},
		{
			name:        "Invalid value - ends with non-alphanumeric",
			label:       "key=invalid-",
			expectedKey: "",
			expectedVal: "",
			expectError: true,
			errorMsg:    "invalid label value: value must consist of alphanumeric characters, '-', '_' or '.', and must start and end with an alphanumeric character",
		},
		{
			name:        "Invalid prefix - too long",
			label:       "a" + string(make([]byte, 253)) + "/n=value", // prefix is 254 chars (1 + 253), which is > 253
			expectedKey: "",
			expectedVal: "",
			expectError: true,
			errorMsg:    "invalid label key: key cannot be longer than 253 characters", // This will hit the overall length check first
		},
		{
			name:        "Invalid prefix - not DNS subdomain",
			label:       "invalid..prefix/name=value",
			expectedKey: "",
			expectedVal: "",
			expectError: true,
			errorMsg:    "invalid label key: prefix must be a valid DNS subdomain",
		},
		{
			name:        "Invalid key - multiple slashes",
			label:       "prefix/middle/name=value",
			expectedKey: "",
			expectedVal: "",
			expectError: true,
			errorMsg:    "invalid label key: key can have at most one '/' separator",
		},
		{
			name:        "Invalid key - name part too long",
			label:       "prefix/" + string(make([]byte, 64)) + "=value",
			expectedKey: "",
			expectedVal: "",
			expectError: true,
			errorMsg:    "invalid label key: name part cannot be longer than 63 characters",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			key, value, err := ParseLabel(tc.label)

			// Check error
			if tc.expectError {
				if err == nil {
					t.Errorf("Expected error but got nil")
				} else if err.Error() != tc.errorMsg {
					t.Errorf("Expected error message '%s', but got '%s'", tc.errorMsg, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("Expected no error but got: %v", err)
				}
			}

			// Check results
			if key != tc.expectedKey {
				t.Errorf("Expected key to be '%s', but got '%s'", tc.expectedKey, key)
			}
			if value != tc.expectedVal {
				t.Errorf("Expected value to be '%s', but got '%s'", tc.expectedVal, value)
			}
		})
	}
}

func TestGetToolType(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		labels   map[string]string
		expected string
	}{
		{
			name: "Tool type exists",
			labels: map[string]string{
				LabelToolType: "mcp",
			},
			expected: "mcp",
		},
		{
			name:     "Tool type doesn't exist",
			labels:   map[string]string{},
			expected: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result := GetToolType(tc.labels)
			if result != tc.expected {
				t.Errorf("Expected tool type to be %s, but got %s", tc.expected, result)
			}
		})
	}
}
