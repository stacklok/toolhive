package config

import (
	"path/filepath"
	"testing"
)

const testNpmrcContent = "//npm.corp.example.com/:_authToken=TOKEN"

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

func TestSetBuildAuthFile(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		fileKey string
		content string
		wantErr bool
	}{
		{
			name:    "valid npmrc",
			fileKey: "npmrc",
			content: testNpmrcContent,
			wantErr: false,
		},
		{
			name:    "valid netrc",
			fileKey: "netrc",
			content: "machine github.com login git password TOKEN",
			wantErr: false,
		},
		{
			name:    "valid yarnrc",
			fileKey: "yarnrc",
			content: "registry \"https://npm.corp.example.com\"",
			wantErr: false,
		},
		{
			name:    "multiline content",
			fileKey: "npmrc",
			content: "//npm.corp.example.com/:_authToken=TOKEN\nalways-auth=true",
			wantErr: false,
		},
		{
			name:    "empty content",
			fileKey: "npmrc",
			content: "",
			wantErr: false,
		},
		{
			name:    "unsupported file type",
			fileKey: "piprc",
			content: "some content",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tempDir := t.TempDir()
			configPath := filepath.Join(tempDir, "config.yaml")
			provider := NewPathProvider(configPath)

			err := setBuildAuthFile(provider, tt.fileKey, tt.content)
			if (err != nil) != tt.wantErr {
				t.Errorf("setBuildAuthFile(%q, %q) error = %v, wantErr %v", tt.fileKey, tt.content, err, tt.wantErr)
			}

			if !tt.wantErr {
				// Verify it was stored
				content, exists := getBuildAuthFile(provider, tt.fileKey)
				if !exists {
					t.Errorf("expected auth file to be stored")
				}
				if content != tt.content {
					t.Errorf("expected content %q, got %q", tt.content, content)
				}
			}
		})
	}
}

func TestGetBuildAuthFile(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.yaml")
	provider := NewPathProvider(configPath)

	// Test getting non-existent file
	_, exists := getBuildAuthFile(provider, "npmrc")
	if exists {
		t.Errorf("expected npmrc to not exist initially")
	}

	// Set a file and get it
	content := testNpmrcContent
	err := setBuildAuthFile(provider, "npmrc", content)
	if err != nil {
		t.Fatalf("failed to set auth file: %v", err)
	}

	got, exists := getBuildAuthFile(provider, "npmrc")
	if !exists {
		t.Errorf("expected npmrc to exist after setting")
	}
	if got != content {
		t.Errorf("expected content %q, got %q", content, got)
	}

	// Getting a different file type should return not found
	_, exists = getBuildAuthFile(provider, "netrc")
	if exists {
		t.Errorf("expected netrc to not exist")
	}
}

func TestGetAllBuildAuthFiles(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.yaml")
	provider := NewPathProvider(configPath)

	// Test empty initial state
	authFiles := getAllBuildAuthFiles(provider)
	if len(authFiles) != 0 {
		t.Errorf("expected no auth files initially, got %d", len(authFiles))
	}

	// Add some auth files
	npmrcContent := testNpmrcContent
	netrcContent := "machine github.com login git password TOKEN"

	err := setBuildAuthFile(provider, "npmrc", npmrcContent)
	if err != nil {
		t.Fatalf("failed to set npmrc: %v", err)
	}

	err = setBuildAuthFile(provider, "netrc", netrcContent)
	if err != nil {
		t.Fatalf("failed to set netrc: %v", err)
	}

	// Get all files
	authFiles = getAllBuildAuthFiles(provider)
	if len(authFiles) != 2 {
		t.Errorf("expected 2 auth files, got %d", len(authFiles))
	}

	if authFiles["npmrc"] != npmrcContent {
		t.Errorf("expected npmrc content %q, got %q", npmrcContent, authFiles["npmrc"])
	}

	if authFiles["netrc"] != netrcContent {
		t.Errorf("expected netrc content %q, got %q", netrcContent, authFiles["netrc"])
	}

	// Verify it returns a copy (mutation doesn't affect original)
	authFiles["new"] = "test"
	authFiles2 := getAllBuildAuthFiles(provider)
	if len(authFiles2) != 2 {
		t.Errorf("expected original to be unchanged, got %d entries", len(authFiles2))
	}
}

func TestUnsetBuildAuthFile(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.yaml")
	provider := NewPathProvider(configPath)

	// Set and verify
	content := testNpmrcContent
	err := setBuildAuthFile(provider, "npmrc", content)
	if err != nil {
		t.Fatalf("failed to set auth file: %v", err)
	}

	_, exists := getBuildAuthFile(provider, "npmrc")
	if !exists {
		t.Fatalf("expected auth file to exist")
	}

	// Unset and verify
	err = unsetBuildAuthFile(provider, "npmrc")
	if err != nil {
		t.Fatalf("failed to unset auth file: %v", err)
	}

	_, exists = getBuildAuthFile(provider, "npmrc")
	if exists {
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

	// Add multiple auth files
	err := setBuildAuthFile(provider, "npmrc", "npmrc content")
	if err != nil {
		t.Fatalf("failed to set npmrc: %v", err)
	}

	err = setBuildAuthFile(provider, "netrc", "netrc content")
	if err != nil {
		t.Fatalf("failed to set netrc: %v", err)
	}

	err = setBuildAuthFile(provider, "yarnrc", "yarnrc content")
	if err != nil {
		t.Fatalf("failed to set yarnrc: %v", err)
	}

	// Verify all were set
	authFiles := getAllBuildAuthFiles(provider)
	if len(authFiles) != 3 {
		t.Fatalf("expected 3 auth files, got %d", len(authFiles))
	}

	// Unset all
	err = unsetAllBuildAuthFiles(provider)
	if err != nil {
		t.Fatalf("failed to unset all auth files: %v", err)
	}

	// Verify all were removed
	authFiles = getAllBuildAuthFiles(provider)
	if len(authFiles) != 0 {
		t.Errorf("expected 0 auth files after unset all, got %d", len(authFiles))
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

func TestSetBuildAuthFile_UpdateExisting(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.yaml")
	provider := NewPathProvider(configPath)

	// Set initial content
	initialContent := "initial content"
	err := setBuildAuthFile(provider, "npmrc", initialContent)
	if err != nil {
		t.Fatalf("failed to set initial content: %v", err)
	}

	// Update with new content
	newContent := "new content"
	err = setBuildAuthFile(provider, "npmrc", newContent)
	if err != nil {
		t.Fatalf("failed to update content: %v", err)
	}

	// Verify content was updated
	content, exists := getBuildAuthFile(provider, "npmrc")
	if !exists {
		t.Fatalf("expected auth file to exist")
	}
	if content != newContent {
		t.Errorf("expected content %q, got %q", newContent, content)
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
