package config

import (
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
