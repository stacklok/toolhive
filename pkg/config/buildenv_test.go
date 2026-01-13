package config

import (
	"path/filepath"
	"testing"
)

func TestValidateBuildEnvKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		key     string
		wantErr bool
	}{
		// Valid keys
		{name: "simple uppercase", key: "NPM_CONFIG_REGISTRY", wantErr: false},
		{name: "with numbers", key: "GO111MODULE", wantErr: false},
		{name: "single letter", key: "A", wantErr: false},
		{name: "all caps with underscore", key: "PIP_INDEX_URL", wantErr: false},
		{name: "uv default index", key: "UV_DEFAULT_INDEX", wantErr: false},
		{name: "goproxy", key: "GOPROXY", wantErr: false},
		{name: "goprivate", key: "GOPRIVATE", wantErr: false},
		{name: "node options", key: "NODE_OPTIONS", wantErr: false},

		// Invalid keys - pattern mismatch
		{name: "lowercase", key: "npm_config_registry", wantErr: true},
		{name: "starts with number", key: "1VAR", wantErr: true},
		{name: "starts with underscore", key: "_VAR", wantErr: true},
		{name: "contains lowercase", key: "NPM_config_REGISTRY", wantErr: true},
		{name: "contains hyphen", key: "NPM-CONFIG", wantErr: true},
		{name: "contains space", key: "NPM CONFIG", wantErr: true},
		{name: "empty string", key: "", wantErr: true},
		{name: "contains dot", key: "NPM.CONFIG", wantErr: true},

		// Reserved keys
		{name: "reserved PATH", key: "PATH", wantErr: true},
		{name: "reserved HOME", key: "HOME", wantErr: true},
		{name: "reserved USER", key: "USER", wantErr: true},
		{name: "reserved SHELL", key: "SHELL", wantErr: true},
		{name: "reserved LD_PRELOAD", key: "LD_PRELOAD", wantErr: true},
		{name: "reserved LD_LIBRARY_PATH", key: "LD_LIBRARY_PATH", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := ValidateBuildEnvKey(tt.key)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateBuildEnvKey(%q) error = %v, wantErr %v", tt.key, err, tt.wantErr)
			}
		})
	}
}

func TestValidateBuildEnvValue(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		value   string
		wantErr bool
	}{
		// Valid values
		{name: "simple URL", value: "https://npm.corp.example.com", wantErr: false},
		{name: "URL with path", value: "https://artifactory.corp.example.com/api/npm/npm-remote/", wantErr: false},
		{name: "URL with port", value: "https://registry.example.com:8443", wantErr: false},
		{name: "simple string", value: "latest", wantErr: false},
		{name: "comma-separated", value: "github.com/myorg/*,gitlab.mycompany.com/*", wantErr: false},
		{name: "memory limit", value: "--max-old-space-size=4096", wantErr: false},
		{name: "empty string", value: "", wantErr: false},
		{name: "with equals sign", value: "key=value", wantErr: false},
		{name: "with single quotes", value: "it's fine", wantErr: false},

		// Invalid values - dangerous characters
		{name: "backtick command substitution", value: "`whoami`", wantErr: true},
		{name: "dollar paren command substitution", value: "$(whoami)", wantErr: true},
		{name: "variable expansion", value: "${HOME}", wantErr: true},
		{name: "backslash escape", value: "test\\nvalue", wantErr: true},
		{name: "newline", value: "test\nvalue", wantErr: true},
		{name: "carriage return", value: "test\rvalue", wantErr: true},
		{name: "double quote", value: "test\"value", wantErr: true},
		{name: "semicolon", value: "test;whoami", wantErr: true},
		{name: "and chain", value: "test&&whoami", wantErr: true},
		{name: "or chain", value: "test||whoami", wantErr: true},
		{name: "pipe", value: "test|whoami", wantErr: true},
		{name: "output redirect", value: "test>file", wantErr: true},
		{name: "input redirect", value: "test<file", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := ValidateBuildEnvValue(tt.value)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateBuildEnvValue(%q) error = %v, wantErr %v", tt.value, err, tt.wantErr)
			}
		})
	}
}

func TestValidateBuildEnvEntry(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		key     string
		value   string
		wantErr bool
	}{
		{
			name:    "valid entry",
			key:     "NPM_CONFIG_REGISTRY",
			value:   "https://npm.corp.example.com",
			wantErr: false,
		},
		{
			name:    "invalid key",
			key:     "npm_config_registry",
			value:   "https://npm.corp.example.com",
			wantErr: true,
		},
		{
			name:    "invalid value",
			key:     "NPM_CONFIG_REGISTRY",
			value:   "$(whoami)",
			wantErr: true,
		},
		{
			name:    "reserved key",
			key:     "PATH",
			value:   "/usr/local/bin",
			wantErr: true,
		},
		{
			name:    "both invalid",
			key:     "path",
			value:   "$(whoami)",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := ValidateBuildEnvEntry(tt.key, tt.value)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateBuildEnvEntry(%q, %q) error = %v, wantErr %v", tt.key, tt.value, err, tt.wantErr)
			}
		})
	}
}

func TestSetBuildEnvFromSecret(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		key        string
		secretName string
		wantErr    bool
	}{
		{
			name:       "valid secret reference",
			key:        "GITHUB_TOKEN",
			secretName: "github-pat",
			wantErr:    false,
		},
		{
			name:       "invalid key",
			key:        "invalid-key",
			secretName: "some-secret",
			wantErr:    true,
		},
		{
			name:       "reserved key",
			key:        "PATH",
			secretName: "some-secret",
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tempDir := t.TempDir()
			configPath := filepath.Join(tempDir, "config.yaml")
			provider := NewPathProvider(configPath)

			err := setBuildEnvFromSecret(provider, tt.key, tt.secretName)
			if (err != nil) != tt.wantErr {
				t.Errorf("setBuildEnvFromSecret(%q, %q) error = %v, wantErr %v", tt.key, tt.secretName, err, tt.wantErr)
			}

			if !tt.wantErr {
				// Verify it was stored
				secretName, exists := getBuildEnvFromSecret(provider, tt.key)
				if !exists {
					t.Errorf("expected secret reference to be stored")
				}
				if secretName != tt.secretName {
					t.Errorf("expected secret name %q, got %q", tt.secretName, secretName)
				}
			}
		})
	}
}

func TestSetBuildEnvFromShell(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		key     string
		wantErr bool
	}{
		{
			name:    "valid shell reference",
			key:     "ARTIFACTORY_API_KEY",
			wantErr: false,
		},
		{
			name:    "invalid key",
			key:     "invalid-key",
			wantErr: true,
		},
		{
			name:    "reserved key",
			key:     "HOME",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tempDir := t.TempDir()
			configPath := filepath.Join(tempDir, "config.yaml")
			provider := NewPathProvider(configPath)

			err := setBuildEnvFromShell(provider, tt.key)
			if (err != nil) != tt.wantErr {
				t.Errorf("setBuildEnvFromShell(%q) error = %v, wantErr %v", tt.key, err, tt.wantErr)
			}

			if !tt.wantErr {
				// Verify it was stored
				exists := getBuildEnvFromShell(provider, tt.key)
				if !exists {
					t.Errorf("expected shell reference to be stored")
				}
			}
		})
	}
}

func TestCheckBuildEnvKeyConflict(t *testing.T) {
	t.Parallel()

	const githubTokenKey = "GITHUB_TOKEN"

	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.yaml")
	provider := NewPathProvider(configPath)

	// Set up a key in each source
	key1 := "NPM_CONFIG_REGISTRY"
	key2 := githubTokenKey
	key3 := "ARTIFACTORY_KEY"

	// Set literal value
	err := setBuildEnv(provider, key1, "https://example.com")
	if err != nil {
		t.Fatalf("failed to set literal value: %v", err)
	}

	// Set secret reference
	err = setBuildEnvFromSecret(provider, key2, "github-pat")
	if err != nil {
		t.Fatalf("failed to set secret reference: %v", err)
	}

	// Set shell reference
	err = setBuildEnvFromShell(provider, key3)
	if err != nil {
		t.Fatalf("failed to set shell reference: %v", err)
	}

	// Test conflicts
	tests := []struct {
		name    string
		key     string
		wantErr bool
	}{
		{
			name:    "conflict with literal value",
			key:     key1,
			wantErr: true,
		},
		{
			name:    "conflict with secret reference",
			key:     key2,
			wantErr: true,
		},
		{
			name:    "conflict with shell reference",
			key:     key3,
			wantErr: true,
		},
		{
			name:    "no conflict",
			key:     "NEW_VAR",
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := checkBuildEnvKeyConflict(provider, tt.key)
			if (err != nil) != tt.wantErr {
				t.Errorf("checkBuildEnvKeyConflict(%q) error = %v, wantErr %v", tt.key, err, tt.wantErr)
			}
		})
	}
}

func TestGetAllBuildEnvFromSecrets(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.yaml")
	provider := NewPathProvider(configPath)

	// Add some secret references
	err := setBuildEnvFromSecret(provider, "GITHUB_TOKEN", "github-pat")
	if err != nil {
		t.Fatalf("failed to set secret reference: %v", err)
	}

	err = setBuildEnvFromSecret(provider, "NPM_TOKEN", "npm-token")
	if err != nil {
		t.Fatalf("failed to set secret reference: %v", err)
	}

	// Get all secrets
	secrets := getAllBuildEnvFromSecrets(provider)
	if len(secrets) != 2 {
		t.Errorf("expected 2 secret references, got %d", len(secrets))
	}

	if secrets["GITHUB_TOKEN"] != "github-pat" {
		t.Errorf("expected GITHUB_TOKEN to reference github-pat, got %s", secrets["GITHUB_TOKEN"])
	}

	if secrets["NPM_TOKEN"] != "npm-token" {
		t.Errorf("expected NPM_TOKEN to reference npm-token, got %s", secrets["NPM_TOKEN"])
	}

	// Verify it returns a copy (mutation doesn't affect original)
	secrets["NEW_KEY"] = "new-secret"
	secrets2 := getAllBuildEnvFromSecrets(provider)
	if len(secrets2) != 2 {
		t.Errorf("expected original to be unchanged, got %d entries", len(secrets2))
	}
}

func TestGetAllBuildEnvFromShell(t *testing.T) {
	t.Parallel()

	const githubTokenKey = "GITHUB_TOKEN"

	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.yaml")
	provider := NewPathProvider(configPath)

	// Add some shell references
	err := setBuildEnvFromShell(provider, githubTokenKey)
	if err != nil {
		t.Fatalf("failed to set shell reference: %v", err)
	}

	err = setBuildEnvFromShell(provider, "ARTIFACTORY_KEY")
	if err != nil {
		t.Fatalf("failed to set shell reference: %v", err)
	}

	// Get all shell references
	shellRefs := getAllBuildEnvFromShell(provider)
	if len(shellRefs) != 2 {
		t.Errorf("expected 2 shell references, got %d", len(shellRefs))
	}

	// Verify it returns a copy
	shellRefs[0] = "MODIFIED"
	shellRefs2 := getAllBuildEnvFromShell(provider)
	if shellRefs2[0] == "MODIFIED" {
		t.Errorf("expected original to be unchanged")
	}
}

func TestUnsetBuildEnvFromSecret(t *testing.T) {
	t.Parallel()

	const githubTokenKey = "GITHUB_TOKEN"

	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.yaml")
	provider := NewPathProvider(configPath)

	key := githubTokenKey

	// Set and verify
	err := setBuildEnvFromSecret(provider, key, "github-pat")
	if err != nil {
		t.Fatalf("failed to set secret reference: %v", err)
	}

	_, exists := getBuildEnvFromSecret(provider, key)
	if !exists {
		t.Fatalf("expected secret reference to exist")
	}

	// Unset and verify
	err = unsetBuildEnvFromSecret(provider, key)
	if err != nil {
		t.Fatalf("failed to unset secret reference: %v", err)
	}

	_, exists = getBuildEnvFromSecret(provider, key)
	if exists {
		t.Errorf("expected secret reference to be removed")
	}
}

func TestUnsetBuildEnvFromShell(t *testing.T) {
	t.Parallel()

	const githubTokenKey = "GITHUB_TOKEN"

	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.yaml")
	provider := NewPathProvider(configPath)

	key := githubTokenKey

	// Set and verify
	err := setBuildEnvFromShell(provider, key)
	if err != nil {
		t.Fatalf("failed to set shell reference: %v", err)
	}

	exists := getBuildEnvFromShell(provider, key)
	if !exists {
		t.Fatalf("expected shell reference to exist")
	}

	// Unset and verify
	err = unsetBuildEnvFromShell(provider, key)
	if err != nil {
		t.Fatalf("failed to unset shell reference: %v", err)
	}

	exists = getBuildEnvFromShell(provider, key)
	if exists {
		t.Errorf("expected shell reference to be removed")
	}
}

func TestSetBuildEnvFromShell_Duplicate(t *testing.T) {
	t.Parallel()

	const githubTokenKey = "GITHUB_TOKEN"

	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.yaml")
	provider := NewPathProvider(configPath)

	key := githubTokenKey

	// Set once
	err := setBuildEnvFromShell(provider, key)
	if err != nil {
		t.Fatalf("failed to set shell reference: %v", err)
	}

	// Set again - should not error, just skip
	err = setBuildEnvFromShell(provider, key)
	if err != nil {
		t.Fatalf("failed to set shell reference again: %v", err)
	}

	// Verify it's only in the list once
	shellRefs := getAllBuildEnvFromShell(provider)
	count := 0
	for _, k := range shellRefs {
		if k == key {
			count++
		}
	}

	if count != 1 {
		t.Errorf("expected key to appear once, got %d times", count)
	}
}
