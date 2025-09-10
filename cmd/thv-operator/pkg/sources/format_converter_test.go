package sources

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/pkg/registry"
)

func TestNewRegistryFormatConverter(t *testing.T) {
	t.Parallel()

	converter := NewRegistryFormatConverter()
	assert.NotNil(t, converter)
	assert.IsType(t, &RegistryFormatConverter{}, converter)
}

func TestRegistryFormatConverter_SupportedFormats(t *testing.T) {
	t.Parallel()

	formats := supportedFormats()

	assert.Len(t, formats, 2)
	assert.Contains(t, formats, mcpv1alpha1.RegistryFormatToolHive)
	assert.Contains(t, formats, mcpv1alpha1.RegistryFormatUpstream)
}

func TestRegistryFormatConverter_DetectFormat(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		builder        func() []byte
		data           []byte // for simple cases
		expectedFormat string
		expectError    bool
		errorContains  string
	}{
		{
			name: "detect toolhive format",
			builder: func() []byte {
				return NewTestRegistryBuilder(mcpv1alpha1.RegistryFormatToolHive).
					WithServerName("test-server").
					BuildJSON()
			},
			expectedFormat: mcpv1alpha1.RegistryFormatToolHive,
			expectError:    false,
		},
		{
			name: "detect upstream format",
			builder: func() []byte {
				return NewTestRegistryBuilder(mcpv1alpha1.RegistryFormatUpstream).
					WithServerName("test-server").
					BuildJSON()
			},
			expectedFormat: mcpv1alpha1.RegistryFormatUpstream,
			expectError:    false,
		},
		{
			name:          "empty data",
			data:          []byte{},
			expectError:   true,
			errorContains: "data cannot be empty",
		},
		{
			name: "invalid data",
			builder: func() []byte {
				return InvalidJSON()
			},
			expectError:   true,
			errorContains: "unable to detect registry format",
		},
		{
			name:          "unknown format",
			data:          []byte(`{"unknown": "format"}`),
			expectError:   true,
			errorContains: "unable to detect registry format",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var data []byte
			if tt.builder != nil {
				data = tt.builder()
			} else {
				data = tt.data
			}

			format, err := detectFormat(data)

			if tt.expectError {
				assert.Error(t, err)
				assert.Empty(t, format)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedFormat, format)
			}
		})
	}
}

func TestRegistryFormatConverter_Convert(t *testing.T) {
	t.Parallel()

	converter := NewRegistryFormatConverter()

	tests := []struct {
		name          string
		builder       func() []byte
		data          []byte // for simple cases
		fromFormat    string
		toFormat      string
		expectError   bool
		errorContains string
		validateFunc  func(t *testing.T, result []byte)
	}{
		{
			name:        "no conversion needed - same format",
			data:        []byte(`{"test": "data"}`),
			fromFormat:  mcpv1alpha1.RegistryFormatToolHive,
			toFormat:    mcpv1alpha1.RegistryFormatToolHive,
			expectError: false,
			validateFunc: func(t *testing.T, result []byte) { //nolint:thelper
				assert.Equal(t, `{"test": "data"}`, string(result))
			},
		},
		{
			name:          "empty data",
			data:          []byte{},
			fromFormat:    mcpv1alpha1.RegistryFormatToolHive,
			toFormat:      mcpv1alpha1.RegistryFormatUpstream,
			expectError:   true,
			errorContains: "input data cannot be empty",
		},
		{
			name:          "unsupported source format",
			data:          []byte(`{"test": "data"}`),
			fromFormat:    "unsupported",
			toFormat:      mcpv1alpha1.RegistryFormatToolHive,
			expectError:   true,
			errorContains: "unsupported format",
		},
		{
			name:          "unsupported target format",
			data:          []byte(`{"test": "data"}`),
			fromFormat:    mcpv1alpha1.RegistryFormatToolHive,
			toFormat:      "unsupported",
			expectError:   true,
			errorContains: "missing properties",
		},
		{
			name:          "invalid source data",
			data:          []byte("invalid json"),
			fromFormat:    mcpv1alpha1.RegistryFormatToolHive,
			toFormat:      mcpv1alpha1.RegistryFormatUpstream,
			expectError:   true,
			errorContains: "source data validation failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var data []byte
			if tt.builder != nil {
				data = tt.builder()
			} else {
				data = tt.data
			}

			result, err := converter.ConvertRaw(data, tt.fromFormat, tt.toFormat)

			if tt.expectError {
				assert.Error(t, err)
				assert.Nil(t, result)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, result)
				if tt.validateFunc != nil {
					tt.validateFunc(t, result)
				}
			}
		})
	}
}

func TestRegistryFormatConverter_ConvertUpstreamToToolhive(t *testing.T) {
	t.Parallel()

	upstreamData := NewTestRegistryBuilder(mcpv1alpha1.RegistryFormatUpstream).
		WithServerName("test-server").
		BuildJSON()

	converter := NewRegistryFormatConverter()
	result, err := converter.ConvertRaw(upstreamData, mcpv1alpha1.RegistryFormatUpstream, mcpv1alpha1.RegistryFormatToolHive)
	require.NoError(t, err)
	require.NotNil(t, result)

	// Parse result to verify structure
	var toolhiveRegistry registry.Registry
	err = json.Unmarshal(result, &toolhiveRegistry)
	require.NoError(t, err)

	// Verify basic structure
	assert.Equal(t, "1.0", toolhiveRegistry.Version)
	assert.NotNil(t, toolhiveRegistry.Servers)

	// Verify the converted server exists
	assert.Contains(t, toolhiveRegistry.Servers, "test-server")
	server := toolhiveRegistry.Servers["test-server"]
	assert.Equal(t, "test-server", server.Name)
	assert.Contains(t, server.Description, "Test server description for test-server")
}

func TestRegistryFormatConverter_ConvertUpstreamToToolhive_InvalidData(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		builder       func() []byte
		errorContains string
	}{
		{
			name: "invalid json",
			builder: func() []byte {
				return InvalidJSON()
			},
			errorContains: "source data validation failed",
		},
		{
			name: "empty array",
			builder: func() []byte {
				return NewTestRegistryBuilder(mcpv1alpha1.RegistryFormatUpstream).
					Empty().
					BuildJSON()
			},
			errorContains: "upstream registry must contain at least one server",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			converter := NewRegistryFormatConverter()
			data := tt.builder()
			result, err := converter.ConvertRaw(data, mcpv1alpha1.RegistryFormatUpstream, mcpv1alpha1.RegistryFormatToolHive)

			assert.Error(t, err)
			assert.Nil(t, result)
			assert.Contains(t, err.Error(), tt.errorContains)
		})
	}
}

func TestRegistryFormatConverter_ConvertToolhiveToUpstream(t *testing.T) {
	t.Parallel()

	toolhiveData := NewTestRegistryBuilder(mcpv1alpha1.RegistryFormatToolHive).
		WithServerName("test-server").
		BuildJSON()

	converter := NewRegistryFormatConverter()
	result, err := converter.ConvertRaw(toolhiveData, mcpv1alpha1.RegistryFormatToolHive, mcpv1alpha1.RegistryFormatUpstream)
	require.NoError(t, err)
	require.NotNil(t, result)

	// Parse result to verify structure
	// Note: Currently the conversion returns the same Registry structure
	// regardless of target format, as noted in the implementation comments
	var toolhiveRegistry registry.Registry
	err = json.Unmarshal(result, &toolhiveRegistry)
	require.NoError(t, err)

	// Verify we have the converted server
	assert.Len(t, toolhiveRegistry.Servers, 1)
	assert.Contains(t, toolhiveRegistry.Servers, "test-server")
	server := toolhiveRegistry.Servers["test-server"]
	assert.Equal(t, "test-server", server.Name)
	assert.Contains(t, server.Description, "Test server description for test-server")
}

func TestRegistryFormatConverter_ConvertToolhiveToUpstream_InvalidData(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		builder       func() []byte
		errorContains string
	}{
		{
			name: "invalid json",
			builder: func() []byte {
				return InvalidJSON()
			},
			errorContains: "source data validation failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			converter := NewRegistryFormatConverter()
			data := tt.builder()
			result, err := converter.ConvertRaw(data, mcpv1alpha1.RegistryFormatToolHive, mcpv1alpha1.RegistryFormatUpstream)
			assert.Error(t, err)
			assert.Nil(t, result)
			assert.Contains(t, err.Error(), tt.errorContains)
		})
	}
}

func TestContains(t *testing.T) {
	t.Parallel()

	slice := []string{"apple", "banana", "cherry"}

	tests := []struct {
		item     string
		expected bool
	}{
		{"apple", true},
		{"banana", true},
		{"cherry", true},
		{"orange", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.item, func(t *testing.T) {
			t.Parallel()

			result := slices.Contains(slice, tt.item)
			assert.Equal(t, tt.expected, result)
		})
	}
}
