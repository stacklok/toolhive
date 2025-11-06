package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/logger"
)

// testProvider is a simple test implementation of Provider
type testProvider struct {
	config      *Config
	updateError error
}

func (p *testProvider) GetConfig() *Config {
	if p.config == nil {
		cfg := createNewConfigWithDefaults()
		p.config = &cfg
	}
	return p.config
}

func (p *testProvider) UpdateConfig(updateFn func(*Config)) error {
	if p.updateError != nil {
		return p.updateError
	}
	cfg := p.GetConfig()
	updateFn(cfg)
	return nil
}

func (p *testProvider) LoadOrCreateConfig() (*Config, error) {
	return p.GetConfig(), nil
}

func (p *testProvider) SetRegistryURL(registryURL string, allowPrivateRegistryIp bool) error {
	return setRegistryURL(p, registryURL, allowPrivateRegistryIp)
}

func (p *testProvider) SetRegistryFile(registryPath string) error {
	return setRegistryFile(p, registryPath)
}

func (p *testProvider) UnsetRegistry() error {
	return unsetRegistry(p)
}

func (p *testProvider) GetRegistryConfig() (url, localPath string, allowPrivateIP bool, registryType string) {
	return getRegistryConfig(p)
}

func (p *testProvider) SetCACert(certPath string) error {
	return setCACert(p, certPath)
}

func (p *testProvider) GetCACert() (certPath string, exists bool, accessible bool) {
	return getCACert(p)
}

func (p *testProvider) UnsetCACert() error {
	return unsetCACert(p)
}

func TestRegisterConfigField(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		spec        ConfigFieldSpec
		shouldPanic bool
		panicMsg    string
	}{
		{
			name: "successful registration",
			spec: ConfigFieldSpec{
				Name: "test-field-" + t.Name(),
				Setter: func(cfg *Config, value string) {
					cfg.CACertificatePath = value
				},
				Getter: func(cfg *Config) string {
					return cfg.CACertificatePath
				},
				Unsetter: func(cfg *Config) {
					cfg.CACertificatePath = ""
				},
			},
			shouldPanic: false,
		},
		{
			name: "empty field name",
			spec: ConfigFieldSpec{
				Name: "",
				Setter: func(cfg *Config, value string) {
					cfg.CACertificatePath = value
				},
				Getter: func(cfg *Config) string {
					return cfg.CACertificatePath
				},
				Unsetter: func(cfg *Config) {
					cfg.CACertificatePath = ""
				},
			},
			shouldPanic: true,
			panicMsg:    "config field name cannot be empty",
		},
		{
			name: "missing setter",
			spec: ConfigFieldSpec{
				Name:   "test-field-no-setter",
				Setter: nil,
				Getter: func(cfg *Config) string {
					return cfg.CACertificatePath
				},
				Unsetter: func(cfg *Config) {
					cfg.CACertificatePath = ""
				},
			},
			shouldPanic: true,
			panicMsg:    "must have a Setter",
		},
		{
			name: "missing getter",
			spec: ConfigFieldSpec{
				Name: "test-field-no-getter",
				Setter: func(cfg *Config, value string) {
					cfg.CACertificatePath = value
				},
				Getter: nil,
				Unsetter: func(cfg *Config) {
					cfg.CACertificatePath = ""
				},
			},
			shouldPanic: true,
			panicMsg:    "must have a Getter",
		},
		{
			name: "missing unsetter",
			spec: ConfigFieldSpec{
				Name: "test-field-no-unsetter",
				Setter: func(cfg *Config, value string) {
					cfg.CACertificatePath = value
				},
				Getter: func(cfg *Config) string {
					return cfg.CACertificatePath
				},
				Unsetter: nil,
			},
			shouldPanic: true,
			panicMsg:    "must have an Unsetter",
		},
	}

	for _, tt := range tests {
		tt := tt // capture range variable
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if tt.shouldPanic {
				// Capture the panic and check the message
				defer func() {
					if r := recover(); r != nil {
						panicMsg := fmt.Sprint(r)
						assert.Contains(t, panicMsg, tt.panicMsg, "panic message should contain expected substring")
					} else {
						t.Error("expected panic but none occurred")
					}
				}()
				RegisterConfigField(tt.spec)
			} else {
				assert.NotPanics(t, func() {
					RegisterConfigField(tt.spec)
				}, "should not panic")

				// Verify field was registered
				spec, exists := GetConfigFieldSpec(tt.spec.Name)
				assert.True(t, exists, "field should be registered")
				assert.Equal(t, tt.spec.Name, spec.Name, "field name should match")
			}
		})
	}
}

func TestGetConfigFieldSpec(t *testing.T) {
	t.Parallel()

	// Register a test field
	testFieldName := "test-get-field-" + t.Name()
	RegisterConfigField(ConfigFieldSpec{
		Name: testFieldName,
		Setter: func(cfg *Config, value string) {
			cfg.CACertificatePath = value
		},
		Getter: func(cfg *Config) string {
			return cfg.CACertificatePath
		},
		Unsetter: func(cfg *Config) {
			cfg.CACertificatePath = ""
		},
	})

	tests := []struct {
		name       string
		fieldName  string
		wantExists bool
	}{
		{
			name:       "existing field",
			fieldName:  testFieldName,
			wantExists: true,
		},
		{
			name:       "non-existent field",
			fieldName:  "non-existent-field",
			wantExists: false,
		},
	}

	for _, tt := range tests {
		tt := tt // capture range variable
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			spec, exists := GetConfigFieldSpec(tt.fieldName)
			assert.Equal(t, tt.wantExists, exists, "existence check should match")
			if tt.wantExists {
				assert.Equal(t, tt.fieldName, spec.Name, "field name should match")
				assert.NotNil(t, spec.Setter, "setter should not be nil")
				assert.NotNil(t, spec.Getter, "getter should not be nil")
				assert.NotNil(t, spec.Unsetter, "unsetter should not be nil")
			}
		})
	}
}

func TestListConfigFields(t *testing.T) {
	t.Parallel()

	// Note: This test checks that built-in fields are registered
	// The actual list may vary based on init() functions
	fields := ListConfigFields()
	assert.NotEmpty(t, fields, "should have registered fields")

	// Check for built-in fields
	fieldMap := make(map[string]bool)
	for _, field := range fields {
		fieldMap[field] = true
	}

	assert.True(t, fieldMap["ca-cert"], "should have ca-cert field")
	assert.True(t, fieldMap["registry-url"], "should have registry-url field")
	assert.True(t, fieldMap["registry-file"], "should have registry-file field")
}

func TestSetConfigField(t *testing.T) {
	t.Parallel()
	logger.Initialize()

	tests := []struct {
		name          string
		fieldName     string
		value         string
		setupProvider func() Provider
		wantErr       bool
		errContains   string
	}{
		{
			name:      "non-existent field",
			fieldName: "non-existent-field",
			value:     "test-value",
			setupProvider: func() Provider {
				return &testProvider{}
			},
			wantErr:     true,
			errContains: "unknown config field",
		},
		{
			name:      "validation failure",
			fieldName: "ca-cert",
			value:     "/non/existent/cert.pem",
			setupProvider: func() Provider {
				return &testProvider{}
			},
			wantErr:     true,
			errContains: "file not found",
		},
		{
			name:      "update config failure",
			fieldName: "test-update-fail-" + t.Name(),
			value:     "test-value",
			setupProvider: func() Provider {
				// Register field before returning provider
				RegisterConfigField(ConfigFieldSpec{
					Name: "test-update-fail-" + t.Name(),
					Setter: func(cfg *Config, value string) {
						cfg.CACertificatePath = value
					},
					Getter: func(cfg *Config) string {
						return cfg.CACertificatePath
					},
					Unsetter: func(cfg *Config) {
						cfg.CACertificatePath = ""
					},
				})
				return &testProvider{updateError: fmt.Errorf("update failed")}
			},
			wantErr:     true,
			errContains: "failed to update configuration",
		},
	}

	for _, tt := range tests {
		tt := tt // capture range variable
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			provider := tt.setupProvider()

			err := SetConfigField(provider, tt.fieldName, tt.value)

			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestGetConfigField(t *testing.T) {
	t.Parallel()
	logger.Initialize()

	tests := []struct {
		name          string
		fieldName     string
		setupProvider func() Provider
		wantValue     string
		wantIsSet     bool
		wantErr       bool
		errContains   string
	}{
		{
			name:      "non-existent field",
			fieldName: "non-existent-field",
			setupProvider: func() Provider {
				return &testProvider{}
			},
			wantErr:     true,
			errContains: "unknown config field",
		},
		{
			name:      "field not set",
			fieldName: "ca-cert",
			setupProvider: func() Provider {
				return &testProvider{
					config: &Config{
						CACertificatePath: "",
					},
				}
			},
			wantValue: "",
			wantIsSet: false,
			wantErr:   false,
		},
		{
			name:      "field is set",
			fieldName: "ca-cert",
			setupProvider: func() Provider {
				return &testProvider{
					config: &Config{
						CACertificatePath: "/path/to/cert.pem",
					},
				}
			},
			wantValue: "/path/to/cert.pem",
			wantIsSet: true,
			wantErr:   false,
		},
	}

	for _, tt := range tests {
		tt := tt // capture range variable
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			provider := tt.setupProvider()

			value, isSet, err := GetConfigField(provider, tt.fieldName)

			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.wantValue, value)
				assert.Equal(t, tt.wantIsSet, isSet)
			}
		})
	}
}

func TestUnsetConfigField(t *testing.T) {
	t.Parallel()
	logger.Initialize()

	tests := []struct {
		name          string
		fieldName     string
		setupProvider func() Provider
		wantErr       bool
		errContains   string
	}{
		{
			name:      "non-existent field",
			fieldName: "non-existent-field",
			setupProvider: func() Provider {
				return &testProvider{}
			},
			wantErr:     true,
			errContains: "unknown config field",
		},
		{
			name:      "successful unset",
			fieldName: "test-unset-success-" + t.Name(),
			setupProvider: func() Provider {
				// Register field before returning provider
				RegisterConfigField(ConfigFieldSpec{
					Name: "test-unset-success-" + t.Name(),
					Setter: func(cfg *Config, value string) {
						cfg.CACertificatePath = value
					},
					Getter: func(cfg *Config) string {
						return cfg.CACertificatePath
					},
					Unsetter: func(cfg *Config) {
						cfg.CACertificatePath = ""
					},
				})
				return &testProvider{
					config: &Config{CACertificatePath: "/old/path"},
				}
			},
			wantErr: false,
		},
		{
			name:      "update config failure",
			fieldName: "test-unset-fail-" + t.Name(),
			setupProvider: func() Provider {
				// Register field before returning provider
				RegisterConfigField(ConfigFieldSpec{
					Name: "test-unset-fail-" + t.Name(),
					Setter: func(cfg *Config, value string) {
						cfg.CACertificatePath = value
					},
					Getter: func(cfg *Config) string {
						return cfg.CACertificatePath
					},
					Unsetter: func(cfg *Config) {
						cfg.CACertificatePath = ""
					},
				})
				return &testProvider{updateError: fmt.Errorf("update failed")}
			},
			wantErr:     true,
			errContains: "failed to update configuration",
		},
	}

	for _, tt := range tests {
		tt := tt // capture range variable
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			provider := tt.setupProvider()

			err := UnsetConfigField(provider, tt.fieldName)

			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
			} else {
				require.NoError(t, err)
				// Verify the field was actually unset
				if tt.name == "successful unset" {
					assert.Equal(t, "", provider.GetConfig().CACertificatePath)
				}
			}
		})
	}
}

func TestFieldRegistryConcurrency(t *testing.T) {
	t.Parallel()

	// Test concurrent reads and writes to the field registry
	var wg sync.WaitGroup
	numGoroutines := 10

	// Concurrent registration
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			fieldName := fmt.Sprintf("concurrent-field-%d", idx)
			RegisterConfigField(ConfigFieldSpec{
				Name: fieldName,
				Setter: func(cfg *Config, value string) {
					cfg.CACertificatePath = value
				},
				Getter: func(cfg *Config) string {
					return cfg.CACertificatePath
				},
				Unsetter: func(cfg *Config) {
					cfg.CACertificatePath = ""
				},
			})
		}(i)
	}

	// Concurrent reads
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = ListConfigFields()
		}()
	}

	wg.Wait()

	// Verify all fields were registered
	fields := ListConfigFields()
	fieldMap := make(map[string]bool)
	for _, field := range fields {
		fieldMap[field] = true
	}

	for i := 0; i < numGoroutines; i++ {
		fieldName := fmt.Sprintf("concurrent-field-%d", i)
		assert.True(t, fieldMap[fieldName], "field %s should be registered", fieldName)
	}
}

func TestSetConfigFieldIntegration(t *testing.T) {
	t.Parallel()
	logger.Initialize()

	// Create a temporary config file
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.yaml")

	// Create a valid CA certificate for testing
	certPath := filepath.Join(tempDir, "test-cert.pem")
	err := os.WriteFile(certPath, []byte(validCACertificate), 0600)
	require.NoError(t, err)

	// Create a provider
	provider := NewPathProvider(configPath)

	// Test setting CA certificate
	err = SetConfigField(provider, "ca-cert", certPath)
	require.NoError(t, err)

	// Verify the field was set
	value, isSet, err := GetConfigField(provider, "ca-cert")
	require.NoError(t, err)
	assert.True(t, isSet)
	assert.Equal(t, certPath, value)

	// Test unsetting CA certificate
	err = UnsetConfigField(provider, "ca-cert")
	require.NoError(t, err)

	// Verify the field was unset
	value, isSet, err = GetConfigField(provider, "ca-cert")
	require.NoError(t, err)
	assert.False(t, isSet)
	assert.Empty(t, value)
}
