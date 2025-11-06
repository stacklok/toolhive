package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/logger"
)

func TestBuiltinFields_CACert(t *testing.T) {
	t.Parallel()
	logger.Initialize()

	tests := []struct {
		name        string
		certType    string // "valid", "invalid", "nonexistent"
		wantErr     bool
		errContains string
	}{
		{
			name:     "valid CA certificate",
			certType: "valid",
			wantErr:  false,
		},
		{
			name:        "non-existent certificate",
			certType:    "nonexistent",
			wantErr:     true,
			errContains: "file not found",
		},
		{
			name:        "invalid certificate format",
			certType:    "invalid",
			wantErr:     true,
			errContains: "invalid CA certificate",
		},
	}

	for _, tt := range tests {
		tt := tt // capture range variable
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Create test files for each subtest
			tempDir := t.TempDir()
			configPath := filepath.Join(tempDir, "config.yaml")

			// Create a valid CA certificate for testing
			certPath := filepath.Join(tempDir, "test-cert.pem")
			err := os.WriteFile(certPath, []byte(validCACertificate), 0600)
			require.NoError(t, err)

			// Create an invalid certificate
			invalidCertPath := filepath.Join(tempDir, "invalid-cert.pem")
			err = os.WriteFile(invalidCertPath, []byte("not a valid certificate"), 0600)
			require.NoError(t, err)

			provider := NewPathProvider(configPath)

			// Determine which cert path to use based on test type
			var testCertPath string
			switch tt.certType {
			case "valid":
				testCertPath = certPath
			case "invalid":
				testCertPath = invalidCertPath
			case "nonexistent":
				testCertPath = "/non/existent/cert.pem"
			}

			err = SetConfigField(provider, "ca-cert", testCertPath)

			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
			} else {
				require.NoError(t, err)

				// Verify the field was set correctly
				value, isSet, err := GetConfigField(provider, "ca-cert")
				require.NoError(t, err)
				assert.True(t, isSet)
				assert.Equal(t, testCertPath, value)

				// Test unset
				err = UnsetConfigField(provider, "ca-cert")
				require.NoError(t, err)

				value, isSet, err = GetConfigField(provider, "ca-cert")
				require.NoError(t, err)
				assert.False(t, isSet)
				assert.Empty(t, value)
			}
		})
	}
}

func TestBuiltinFields_RegistryFile(t *testing.T) {
	t.Parallel()
	logger.Initialize()

	tests := []struct {
		name        string
		fileType    string // "valid", "invalid-json", "non-json", "nonexistent"
		wantErr     bool
		errContains string
	}{
		{
			name:     "valid registry file",
			fileType: "valid",
			wantErr:  false,
		},
		{
			name:        "non-existent file",
			fileType:    "nonexistent",
			wantErr:     true,
			errContains: "file not found",
		},
		{
			name:        "invalid JSON content",
			fileType:    "invalid-json",
			wantErr:     true,
			errContains: "invalid JSON format",
		},
		{
			name:        "non-JSON file extension",
			fileType:    "non-json",
			wantErr:     true,
			errContains: "must be a JSON file",
		},
	}

	for _, tt := range tests {
		tt := tt // capture range variable
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Create test files for each subtest
			tempDir := t.TempDir()
			configPath := filepath.Join(tempDir, "config.yaml")

			// Create a valid JSON registry file
			validRegistryPath := filepath.Join(tempDir, "registry.json")
			validJSON := `{"servers": [{"name": "test", "url": "https://example.com"}]}`
			err := os.WriteFile(validRegistryPath, []byte(validJSON), 0600)
			require.NoError(t, err)

			// Create an invalid JSON file
			invalidJSONPath := filepath.Join(tempDir, "invalid.json")
			err = os.WriteFile(invalidJSONPath, []byte("not valid json"), 0600)
			require.NoError(t, err)

			// Create a non-JSON file
			nonJSONPath := filepath.Join(tempDir, "registry.txt")
			err = os.WriteFile(nonJSONPath, []byte("some text"), 0600)
			require.NoError(t, err)

			provider := NewPathProvider(configPath)

			// Determine which registry path to use based on file type
			var testRegistryPath string
			switch tt.fileType {
			case "valid":
				testRegistryPath = validRegistryPath
			case "invalid-json":
				testRegistryPath = invalidJSONPath
			case "non-json":
				testRegistryPath = nonJSONPath
			case "nonexistent":
				testRegistryPath = "/non/existent/registry.json"
			}

			err = SetConfigField(provider, "registry-file", testRegistryPath)

			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
			} else {
				require.NoError(t, err)

				// Verify the field was set correctly (should be absolute path)
				value, isSet, err := GetConfigField(provider, "registry-file")
				require.NoError(t, err)
				assert.True(t, isSet)
				assert.True(t, filepath.IsAbs(value), "path should be absolute")

				// The value might be cleaned/absolute, so check the base name
				assert.Equal(t, filepath.Base(testRegistryPath), filepath.Base(value))

				// Verify URL is cleared when file is set
				cfg := provider.GetConfig()
				assert.Empty(t, cfg.RegistryUrl, "registry URL should be cleared when file is set")

				// Test unset
				err = UnsetConfigField(provider, "registry-file")
				require.NoError(t, err)

				value, isSet, err = GetConfigField(provider, "registry-file")
				require.NoError(t, err)
				assert.False(t, isSet)
				assert.Empty(t, value)
			}
		})
	}
}

func TestBuiltinFields_RegistryURL(t *testing.T) {
	t.Parallel()
	logger.Initialize()

	tests := []struct {
		name             string
		registryURL      string
		wantErr          bool
		errContains      string
		expectInsecure   bool
		expectedStored   string
		expectPrivateSet bool
	}{
		{
			name:           "valid HTTPS URL",
			registryURL:    "https://registry.example.com/servers",
			wantErr:        false,
			expectedStored: "https://registry.example.com/servers",
		},
		{
			name:             "HTTP URL with insecure flag",
			registryURL:      "http://registry.example.com/servers|insecure",
			wantErr:          false,
			expectInsecure:   true,
			expectedStored:   "http://registry.example.com/servers|insecure",
			expectPrivateSet: true,
		},
		{
			name:        "invalid URL format",
			registryURL: "not-a-url",
			wantErr:     true,
			errContains: "invalid registry URL",
		},
		{
			name:        "HTTP without insecure flag",
			registryURL: "http://registry.example.com/servers",
			wantErr:     true,
			errContains: "invalid registry URL",
		},
	}

	for _, tt := range tests {
		tt := tt // capture range variable
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Create a separate config file for each test case to avoid shared state
			tempDir := t.TempDir()
			configPath := filepath.Join(tempDir, "config.yaml")
			provider := NewPathProvider(configPath)

			err := SetConfigField(provider, "registry-url", tt.registryURL)

			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
			} else {
				require.NoError(t, err)

				// Verify the field was set correctly
				value, isSet, err := GetConfigField(provider, "registry-url")
				require.NoError(t, err)
				assert.True(t, isSet)
				assert.Equal(t, tt.expectedStored, value)

				// Verify file path is cleared when URL is set
				cfg := provider.GetConfig()
				assert.Empty(t, cfg.LocalRegistryPath, "registry file should be cleared when URL is set")

				// Verify AllowPrivateRegistryIp is set correctly
				if tt.expectPrivateSet {
					assert.True(t, cfg.AllowPrivateRegistryIp, "AllowPrivateRegistryIp should be set for insecure URLs")
				}

				// Test unset
				err = UnsetConfigField(provider, "registry-url")
				require.NoError(t, err)

				value, isSet, err = GetConfigField(provider, "registry-url")
				require.NoError(t, err)
				assert.False(t, isSet)
				assert.Empty(t, value)

				// Verify AllowPrivateRegistryIp is reset
				cfg = provider.GetConfig()
				assert.False(t, cfg.AllowPrivateRegistryIp, "AllowPrivateRegistryIp should be reset after unset")
			}
		})
	}
}

func TestBuiltinFields_MutualExclusivity(t *testing.T) {
	t.Parallel()
	logger.Initialize()

	// Create a temporary config file
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.yaml")

	// Create a valid JSON registry file
	validRegistryPath := filepath.Join(tempDir, "registry.json")
	validJSON := `{"servers": []}`
	err := os.WriteFile(validRegistryPath, []byte(validJSON), 0600)
	require.NoError(t, err)

	provider := NewPathProvider(configPath)

	// Set registry URL
	err = SetConfigField(provider, "registry-url", "https://registry.example.com")
	require.NoError(t, err)

	// Verify URL is set
	cfg := provider.GetConfig()
	assert.NotEmpty(t, cfg.RegistryUrl)
	assert.Empty(t, cfg.LocalRegistryPath)

	// Set registry file (should clear URL)
	err = SetConfigField(provider, "registry-file", validRegistryPath)
	require.NoError(t, err)

	// Verify file is set and URL is cleared
	cfg = provider.GetConfig()
	assert.Empty(t, cfg.RegistryUrl)
	assert.NotEmpty(t, cfg.LocalRegistryPath)

	// Set registry URL again (should clear file)
	err = SetConfigField(provider, "registry-url", "https://registry2.example.com")
	require.NoError(t, err)

	// Verify URL is set and file is cleared
	cfg = provider.GetConfig()
	assert.NotEmpty(t, cfg.RegistryUrl)
	assert.Empty(t, cfg.LocalRegistryPath)
}

func TestBuiltinFields_AllFieldsRegistered(t *testing.T) {
	t.Parallel()

	expectedFields := []string{
		"ca-cert",
		"registry-url",
		"registry-file",
	}

	registeredFields := ListConfigFields()
	fieldMap := make(map[string]bool)
	for _, field := range registeredFields {
		fieldMap[field] = true
	}

	for _, expectedField := range expectedFields {
		assert.True(t, fieldMap[expectedField], "field %q should be registered", expectedField)

		// Verify the field has all required components
		spec, exists := GetConfigFieldSpec(expectedField)
		require.True(t, exists, "field %q should be registered", expectedField)
		assert.NotEmpty(t, spec.Name, "field %q should have a name", expectedField)
		assert.NotNil(t, spec.Setter, "field %q should have a setter", expectedField)
		assert.NotNil(t, spec.Getter, "field %q should have a getter", expectedField)
		assert.NotNil(t, spec.Unsetter, "field %q should have an unsetter", expectedField)
	}
}
