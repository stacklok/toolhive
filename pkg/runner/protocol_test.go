package runner

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stacklok/toolhive/pkg/container/templates"
)

func TestIsLocalGoPath(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		expected bool
	}{
		{
			name:     "current directory",
			path:     ".",
			expected: true,
		},
		{
			name:     "relative path with ./",
			path:     "./cmd/server",
			expected: true,
		},
		{
			name:     "relative path with ../",
			path:     "../other-project",
			expected: true,
		},
		{
			name:     "absolute path",
			path:     "/home/user/project",
			expected: true,
		},
		{
			name:     "remote package",
			path:     "github.com/example/package",
			expected: false,
		},
		{
			name:     "remote package with version",
			path:     "github.com/example/package@v1.0.0",
			expected: false,
		},
		{
			name:     "simple package name",
			path:     "mypackage",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isLocalGoPath(tt.path)
			if result != tt.expected {
				t.Errorf("isLocalGoPath(%q) = %v, want %v", tt.path, result, tt.expected)
			}
		})
	}
}

func TestPackageNameToImageName(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "simple package name",
			input:    "mypackage",
			expected: "mypackage",
		},
		{
			name:     "package with slashes",
			input:    "github.com/user/repo",
			expected: "github-com-user-repo",
		},
		{
			name:     "package with version",
			input:    "github.com/user/repo@v1.0.0",
			expected: "github-com-user-repo-v1-0-0",
		},
		{
			name:     "relative path with ./",
			input:    "./cmd/server",
			expected: "cmd-server",
		},
		{
			name:     "relative path with ../",
			input:    "../other-project",
			expected: "other-project",
		},
		{
			name:     "current directory",
			input:    ".",
			expected: "toolhive-container",
		},
		{
			name:     "path with dots",
			input:    "./my.project",
			expected: "my-project",
		},
		{
			name:     "complex path",
			input:    "./cmd/my.server/main",
			expected: "cmd-my-server-main",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := packageNameToImageName(tt.input)
			if result != tt.expected {
				t.Errorf("packageNameToImageName(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestShouldSkipPath(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		expected bool
	}{
		{
			name:     "git directory",
			path:     ".git",
			expected: true,
		},
		{
			name:     "nested git directory",
			path:     "subdir/.git",
			expected: true,
		},
		{
			name:     "node_modules",
			path:     "node_modules",
			expected: true,
		},
		{
			name:     "vendor directory",
			path:     "vendor",
			expected: true,
		},
		{
			name:     "vscode directory",
			path:     ".vscode",
			expected: true,
		},
		{
			name:     "dockerfile",
			path:     "Dockerfile",
			expected: true,
		},
		{
			name:     "gitignore file",
			path:     ".gitignore",
			expected: true,
		},
		{
			name:     "regular go file",
			path:     "main.go",
			expected: false,
		},
		{
			name:     "regular directory",
			path:     "cmd",
			expected: false,
		},
		{
			name:     "nested go file",
			path:     "cmd/server/main.go",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := shouldSkipPath(tt.path)
			if result != tt.expected {
				t.Errorf("shouldSkipPath(%q) = %v, want %v", tt.path, result, tt.expected)
			}
		})
	}
}

func TestCopyLocalSource(t *testing.T) {
	// Create a temporary source directory
	sourceDir, err := os.MkdirTemp("", "test-source-")
	if err != nil {
		t.Fatalf("Failed to create temp source dir: %v", err)
	}
	defer os.RemoveAll(sourceDir)

	// Create a temporary destination directory
	destDir, err := os.MkdirTemp("", "test-dest-")
	if err != nil {
		t.Fatalf("Failed to create temp dest dir: %v", err)
	}
	defer os.RemoveAll(destDir)

	// Create test files in source directory
	testFiles := map[string]string{
		"main.go":            "package main\n\nfunc main() {}\n",
		"go.mod":             "module test\n\ngo 1.21\n",
		"cmd/server/main.go": "package main\n\nfunc main() {}\n",
		"README.md":          "# Test Project\n",
	}

	// Create directories that should be skipped
	skipDirs := []string{
		".git",
		"node_modules",
		"vendor",
		".vscode",
	}

	for _, dir := range skipDirs {
		dirPath := filepath.Join(sourceDir, dir)
		if err := os.MkdirAll(dirPath, 0755); err != nil {
			t.Fatalf("Failed to create skip dir %s: %v", dir, err)
		}
		// Add a file in the skip directory
		skipFile := filepath.Join(dirPath, "should-not-copy.txt")
		if err := os.WriteFile(skipFile, []byte("should not be copied"), 0644); err != nil {
			t.Fatalf("Failed to create skip file: %v", err)
		}
	}

	// Create test files
	for filePath, content := range testFiles {
		fullPath := filepath.Join(sourceDir, filePath)
		if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
			t.Fatalf("Failed to create dir for %s: %v", filePath, err)
		}
		if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
			t.Fatalf("Failed to create file %s: %v", filePath, err)
		}
	}

	// Test copying
	err = copyLocalSource(sourceDir, destDir)
	if err != nil {
		t.Fatalf("copyLocalSource failed: %v", err)
	}

	// Verify that expected files were copied
	for filePath, expectedContent := range testFiles {
		destPath := filepath.Join(destDir, filePath)
		content, err := os.ReadFile(destPath)
		if err != nil {
			t.Errorf("Failed to read copied file %s: %v", filePath, err)
			continue
		}
		if string(content) != expectedContent {
			t.Errorf("File %s content mismatch. Got %q, want %q", filePath, string(content), expectedContent)
		}
	}

	// Verify that skip directories were not copied
	for _, dir := range skipDirs {
		destPath := filepath.Join(destDir, dir)
		if _, err := os.Stat(destPath); !os.IsNotExist(err) {
			t.Errorf("Skip directory %s was copied when it shouldn't have been", dir)
		}
	}
}

func TestIsImageProtocolScheme(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{
			name:     "uvx scheme",
			input:    "uvx://package-name",
			expected: true,
		},
		{
			name:     "npx scheme",
			input:    "npx://package-name",
			expected: true,
		},
		{
			name:     "go scheme",
			input:    "go://package-name",
			expected: true,
		},
		{
			name:     "go scheme with local path",
			input:    "go://./cmd/server",
			expected: true,
		},
		{
			name:     "regular image name",
			input:    "docker.io/library/alpine:latest",
			expected: false,
		},
		{
			name:     "registry server name",
			input:    "fetch",
			expected: false,
		},
		{
			name:     "empty string",
			input:    "",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsImageProtocolScheme(tt.input)
			if result != tt.expected {
				t.Errorf("IsImageProtocolScheme(%q) = %v, want %v", tt.input, result, tt.expected)
			}
		})
	}
}

func TestTemplateDataWithLocalPath(t *testing.T) {
	tests := []struct {
		name        string
		packageName string
		expected    templates.TemplateData
	}{
		{
			name:        "remote package",
			packageName: "github.com/example/package",
			expected: templates.TemplateData{
				MCPPackage:  "github.com/example/package",
				MCPArgs:     []string{},
				IsLocalPath: false,
			},
		},
		{
			name:        "local relative path",
			packageName: "./cmd/server",
			expected: templates.TemplateData{
				MCPPackage:  "./cmd/server",
				MCPArgs:     []string{},
				IsLocalPath: true,
			},
		},
		{
			name:        "current directory",
			packageName: ".",
			expected: templates.TemplateData{
				MCPPackage:  ".",
				MCPArgs:     []string{},
				IsLocalPath: true,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Test the logic that would be used in HandleProtocolScheme
			isLocalPath := isLocalGoPath(tt.packageName)

			templateData := templates.TemplateData{
				MCPPackage:  tt.packageName,
				MCPArgs:     []string{},
				IsLocalPath: isLocalPath,
			}

			if templateData.MCPPackage != tt.expected.MCPPackage {
				t.Errorf("MCPPackage = %q, want %q", templateData.MCPPackage, tt.expected.MCPPackage)
			}
			if templateData.IsLocalPath != tt.expected.IsLocalPath {
				t.Errorf("IsLocalPath = %v, want %v", templateData.IsLocalPath, tt.expected.IsLocalPath)
			}
		})
	}
}
