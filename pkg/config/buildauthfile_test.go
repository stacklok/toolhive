package config

import (
	"path/filepath"
	"testing"
)

func TestValidateBuildAuthFileName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		// Valid file types
		{name: "npmrc", input: "npmrc", wantErr: false},
		{name: "netrc", input: "netrc", wantErr: false},
		{name: "yarnrc", input: "yarnrc", wantErr: false},

		// Invalid file types
		{name: "unsupported file type", input: "piprc", wantErr: true},
		{name: "empty string", input: "", wantErr: true},
		{name: "random string", input: "foo", wantErr: true},
		{name: "uppercase NPMRC", input: "NPMRC", wantErr: true},
		{name: "with dot prefix", input: ".npmrc", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := ValidateBuildAuthFileName(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateBuildAuthFileName(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
		})
	}
}

func TestBuildAuthFileSecretName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		fileType string
		expected string
	}{
		{"npmrc", "BUILD_AUTH_FILE_npmrc"},
		{"netrc", "BUILD_AUTH_FILE_netrc"},
		{"yarnrc", "BUILD_AUTH_FILE_yarnrc"},
	}

	for _, tt := range tests {
		t.Run(tt.fileType, func(t *testing.T) {
			t.Parallel()

			result := BuildAuthFileSecretName(tt.fileType)
			if result != tt.expected {
				t.Errorf("BuildAuthFileSecretName(%q) = %q, want %q", tt.fileType, result, tt.expected)
			}
		})
	}
}

func TestMarkBuildAuthFileConfigured(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		fileKey string
		wantErr bool
	}{
		{
			name:    "valid npmrc",
			fileKey: "npmrc",
			wantErr: false,
		},
		{
			name:    "valid netrc",
			fileKey: "netrc",
			wantErr: false,
		},
		{
			name:    "valid yarnrc",
			fileKey: "yarnrc",
			wantErr: false,
		},
		{
			name:    "unsupported file type",
			fileKey: "piprc",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tempDir := t.TempDir()
			configPath := filepath.Join(tempDir, "config.yaml")
			provider := NewPathProvider(configPath)

			err := markBuildAuthFileConfigured(provider, tt.fileKey)
			if (err != nil) != tt.wantErr {
				t.Errorf("markBuildAuthFileConfigured(%q) error = %v, wantErr %v", tt.fileKey, err, tt.wantErr)
			}

			if !tt.wantErr {
				// Verify it was marked as configured
				if !isBuildAuthFileConfigured(provider, tt.fileKey) {
					t.Errorf("expected auth file to be marked as configured")
				}
			}
		})
	}
}

func TestIsBuildAuthFileConfigured(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.yaml")
	provider := NewPathProvider(configPath)

	// Test getting non-configured file
	if isBuildAuthFileConfigured(provider, "npmrc") {
		t.Errorf("expected npmrc to not be configured initially")
	}

	// Mark a file as configured and check it
	err := markBuildAuthFileConfigured(provider, "npmrc")
	if err != nil {
		t.Fatalf("failed to mark auth file: %v", err)
	}

	if !isBuildAuthFileConfigured(provider, "npmrc") {
		t.Errorf("expected npmrc to be configured after marking")
	}

	// A different file type should not be configured
	if isBuildAuthFileConfigured(provider, "netrc") {
		t.Errorf("expected netrc to not be configured")
	}
}

func TestGetConfiguredBuildAuthFiles(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.yaml")
	provider := NewPathProvider(configPath)

	// Test empty initial state
	configuredFiles := getConfiguredBuildAuthFiles(provider)
	if len(configuredFiles) != 0 {
		t.Errorf("expected no auth files initially, got %d", len(configuredFiles))
	}

	// Mark some auth files as configured
	err := markBuildAuthFileConfigured(provider, "npmrc")
	if err != nil {
		t.Fatalf("failed to mark npmrc: %v", err)
	}

	err = markBuildAuthFileConfigured(provider, "netrc")
	if err != nil {
		t.Fatalf("failed to mark netrc: %v", err)
	}

	// Get all configured files
	configuredFiles = getConfiguredBuildAuthFiles(provider)
	if len(configuredFiles) != 2 {
		t.Errorf("expected 2 configured auth files, got %d", len(configuredFiles))
	}

	// Check that both files are in the list
	hasNpmrc := false
	hasNetrc := false
	for _, f := range configuredFiles {
		if f == "npmrc" {
			hasNpmrc = true
		}
		if f == "netrc" {
			hasNetrc = true
		}
	}
	if !hasNpmrc {
		t.Errorf("expected npmrc to be in configured files")
	}
	if !hasNetrc {
		t.Errorf("expected netrc to be in configured files")
	}
}

func TestUnsetBuildAuthFile(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.yaml")
	provider := NewPathProvider(configPath)

	// Mark and verify
	err := markBuildAuthFileConfigured(provider, "npmrc")
	if err != nil {
		t.Fatalf("failed to mark auth file: %v", err)
	}

	if !isBuildAuthFileConfigured(provider, "npmrc") {
		t.Fatalf("expected auth file to be configured")
	}

	// Unset and verify
	err = unsetBuildAuthFile(provider, "npmrc")
	if err != nil {
		t.Fatalf("failed to unset auth file: %v", err)
	}

	if isBuildAuthFileConfigured(provider, "npmrc") {
		t.Errorf("expected auth file to be removed")
	}
}

func TestUnsetBuildAuthFile_NotExist(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.yaml")
	provider := NewPathProvider(configPath)

	// Unsetting a non-existent file should not error
	err := unsetBuildAuthFile(provider, "npmrc")
	if err != nil {
		t.Errorf("expected no error when unsetting non-existent file, got %v", err)
	}
}

func TestUnsetAllBuildAuthFiles(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.yaml")
	provider := NewPathProvider(configPath)

	// Mark multiple auth files
	err := markBuildAuthFileConfigured(provider, "npmrc")
	if err != nil {
		t.Fatalf("failed to mark npmrc: %v", err)
	}

	err = markBuildAuthFileConfigured(provider, "netrc")
	if err != nil {
		t.Fatalf("failed to mark netrc: %v", err)
	}

	err = markBuildAuthFileConfigured(provider, "yarnrc")
	if err != nil {
		t.Fatalf("failed to mark yarnrc: %v", err)
	}

	// Verify all were marked
	configuredFiles := getConfiguredBuildAuthFiles(provider)
	if len(configuredFiles) != 3 {
		t.Fatalf("expected 3 auth files, got %d", len(configuredFiles))
	}

	// Unset all
	err = unsetAllBuildAuthFiles(provider)
	if err != nil {
		t.Fatalf("failed to unset all auth files: %v", err)
	}

	// Verify all were removed
	configuredFiles = getConfiguredBuildAuthFiles(provider)
	if len(configuredFiles) != 0 {
		t.Errorf("expected 0 auth files after unset all, got %d", len(configuredFiles))
	}
}

func TestUnsetAllBuildAuthFiles_Empty(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.yaml")
	provider := NewPathProvider(configPath)

	// Unsetting when empty should not error
	err := unsetAllBuildAuthFiles(provider)
	if err != nil {
		t.Errorf("expected no error when unsetting empty auth files, got %v", err)
	}
}

func TestMarkBuildAuthFileConfigured_UpdateExisting(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.yaml")
	provider := NewPathProvider(configPath)

	// Mark initial
	err := markBuildAuthFileConfigured(provider, "npmrc")
	if err != nil {
		t.Fatalf("failed to mark initially: %v", err)
	}

	// Mark again (should not error)
	err = markBuildAuthFileConfigured(provider, "npmrc")
	if err != nil {
		t.Fatalf("failed to mark again: %v", err)
	}

	// Verify still configured
	if !isBuildAuthFileConfigured(provider, "npmrc") {
		t.Fatalf("expected auth file to still be configured")
	}
}

func TestSupportedAuthFiles(t *testing.T) {
	t.Parallel()

	// Verify all supported file types have expected paths
	expectedPaths := map[string]string{
		"npmrc":  "/root/.npmrc",
		"netrc":  "/root/.netrc",
		"yarnrc": "/root/.yarnrc",
	}

	for fileType, expectedPath := range expectedPaths {
		t.Run(fileType, func(t *testing.T) {
			t.Parallel()

			actualPath, ok := SupportedAuthFiles[fileType]
			if !ok {
				t.Errorf("expected %s to be a supported auth file", fileType)
			}
			if actualPath != expectedPath {
				t.Errorf("expected path %q for %s, got %q", expectedPath, fileType, actualPath)
			}
		})
	}

	// Verify the count matches
	if len(SupportedAuthFiles) != len(expectedPaths) {
		t.Errorf("expected %d supported auth files, got %d", len(expectedPaths), len(SupportedAuthFiles))
	}
}
