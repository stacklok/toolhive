package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateFilePath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		setupFunc func(t *testing.T) string
		wantErr   bool
		errMsg    string
	}{
		{
			name: "valid file path",
			setupFunc: func(t *testing.T) string {
				t.Helper()
				tmpFile, err := os.CreateTemp("", "test-*.txt")
				require.NoError(t, err)
				t.Cleanup(func() { os.Remove(tmpFile.Name()) })
				return tmpFile.Name()
			},
			wantErr: false,
		},
		{
			name: "non-existent file",
			setupFunc: func(t *testing.T) string {
				t.Helper()
				return "/nonexistent/path/to/file.txt"
			},
			wantErr: true,
			errMsg:  "file not found or not accessible",
		},
		{
			name: "file path with dots gets cleaned",
			setupFunc: func(t *testing.T) string {
				t.Helper()
				tmpFile, err := os.CreateTemp("", "test-*.txt")
				require.NoError(t, err)
				t.Cleanup(func() { os.Remove(tmpFile.Name()) })
				// Add unnecessary path elements
				dir := filepath.Dir(tmpFile.Name())
				base := filepath.Base(tmpFile.Name())
				return filepath.Join(dir, ".", base)
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			path := tt.setupFunc(t)
			cleanPath, err := validateFilePath(path)

			if tt.wantErr {
				assert.Error(t, err)
				if tt.errMsg != "" {
					assert.Contains(t, err.Error(), tt.errMsg)
				}
				assert.Empty(t, cleanPath)
			} else {
				assert.NoError(t, err)
				assert.NotEmpty(t, cleanPath)
				assert.Equal(t, filepath.Clean(path), cleanPath)
			}
		})
	}
}

func TestValidateFileExists(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		setupFunc func(t *testing.T) string
		wantErr   bool
		errMsg    string
	}{
		{
			name: "existing file",
			setupFunc: func(t *testing.T) string {
				t.Helper()
				tmpFile, err := os.CreateTemp("", "test-*.txt")
				require.NoError(t, err)
				t.Cleanup(func() { os.Remove(tmpFile.Name()) })
				return tmpFile.Name()
			},
			wantErr: false,
		},
		{
			name: "non-existent file",
			setupFunc: func(t *testing.T) string {
				t.Helper()
				return "/nonexistent/file.txt"
			},
			wantErr: true,
			errMsg:  "file not found or not accessible",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			path := tt.setupFunc(t)
			err := validateFileExists(path)

			if tt.wantErr {
				assert.Error(t, err)
				if tt.errMsg != "" {
					assert.Contains(t, err.Error(), tt.errMsg)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestReadFile(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		setupFunc   func(t *testing.T) string
		wantContent string
		wantErr     bool
		errMsg      string
	}{
		{
			name: "read valid file",
			setupFunc: func(t *testing.T) string {
				t.Helper()
				tmpFile, err := os.CreateTemp("", "test-*.txt")
				require.NoError(t, err)
				_, err = tmpFile.WriteString("test content")
				require.NoError(t, err)
				tmpFile.Close()
				t.Cleanup(func() { os.Remove(tmpFile.Name()) })
				return tmpFile.Name()
			},
			wantContent: "test content",
			wantErr:     false,
		},
		{
			name: "read non-existent file",
			setupFunc: func(t *testing.T) string {
				t.Helper()
				return "/nonexistent/file.txt"
			},
			wantErr: true,
			errMsg:  "failed to read file",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			path := tt.setupFunc(t)
			data, err := readFile(path)

			if tt.wantErr {
				assert.Error(t, err)
				if tt.errMsg != "" {
					assert.Contains(t, err.Error(), tt.errMsg)
				}
				assert.Nil(t, data)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.wantContent, string(data))
			}
		})
	}
}

func TestValidateJSONFile(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		setupFunc func(t *testing.T) string
		wantErr   bool
		errMsg    string
	}{
		{
			name: "valid JSON file",
			setupFunc: func(t *testing.T) string {
				t.Helper()
				tmpFile, err := os.CreateTemp("", "test-*.json")
				require.NoError(t, err)
				_, err = tmpFile.WriteString(`{"key": "value"}`)
				require.NoError(t, err)
				tmpFile.Close()
				t.Cleanup(func() { os.Remove(tmpFile.Name()) })
				return tmpFile.Name()
			},
			wantErr: false,
		},
		{
			name: "invalid JSON content",
			setupFunc: func(t *testing.T) string {
				t.Helper()
				tmpFile, err := os.CreateTemp("", "test-*.json")
				require.NoError(t, err)
				_, err = tmpFile.WriteString(`{invalid json}`)
				require.NoError(t, err)
				tmpFile.Close()
				t.Cleanup(func() { os.Remove(tmpFile.Name()) })
				return tmpFile.Name()
			},
			wantErr: true,
			errMsg:  "invalid JSON format",
		},
		{
			name: "non-JSON file extension",
			setupFunc: func(t *testing.T) string {
				t.Helper()
				tmpFile, err := os.CreateTemp("", "test-*.txt")
				require.NoError(t, err)
				_, err = tmpFile.WriteString(`{"key": "value"}`)
				require.NoError(t, err)
				tmpFile.Close()
				t.Cleanup(func() { os.Remove(tmpFile.Name()) })
				return tmpFile.Name()
			},
			wantErr: true,
			errMsg:  errJSONExtensionOnly,
		},
		{
			name: "non-existent JSON file",
			setupFunc: func(t *testing.T) string {
				t.Helper()
				return "/nonexistent/file.json"
			},
			wantErr: true,
			errMsg:  "failed to read file",
		},
		{
			name: "JSON file with .JSON extension (uppercase)",
			setupFunc: func(t *testing.T) string {
				t.Helper()
				tmpFile, err := os.CreateTemp("", "test-*.JSON")
				require.NoError(t, err)
				_, err = tmpFile.WriteString(`{"key": "value"}`)
				require.NoError(t, err)
				tmpFile.Close()
				t.Cleanup(func() { os.Remove(tmpFile.Name()) })
				return tmpFile.Name()
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			path := tt.setupFunc(t)
			err := validateJSONFile(path)

			if tt.wantErr {
				assert.Error(t, err)
				if tt.errMsg != "" {
					assert.Contains(t, err.Error(), tt.errMsg)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidateURLScheme(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		url           string
		allowInsecure bool
		wantErr       bool
		errMsg        string
	}{
		{
			name:          "valid https URL",
			url:           "https://example.com",
			allowInsecure: false,
			wantErr:       false,
		},
		{
			name:          "valid http URL with allowInsecure",
			url:           "http://example.com",
			allowInsecure: true,
			wantErr:       false,
		},
		{
			name:          "invalid http URL without allowInsecure",
			url:           "http://example.com",
			allowInsecure: false,
			wantErr:       true,
			errMsg:        "URL must start with https://",
		},
		{
			name:          "invalid scheme - ftp",
			url:           "ftp://example.com",
			allowInsecure: false,
			wantErr:       true,
			errMsg:        "URL must start with https://",
		},
		{
			name:          "invalid scheme - ftp with allowInsecure",
			url:           "ftp://example.com",
			allowInsecure: true,
			wantErr:       true,
			errMsg:        "URL must start with http:// or https://",
		},
		{
			name:          "malformed URL",
			url:           "://invalid",
			allowInsecure: false,
			wantErr:       true,
			errMsg:        "invalid URL format",
		},
		{
			name:          "valid https URL with path",
			url:           "https://example.com/path/to/registry",
			allowInsecure: false,
			wantErr:       false,
		},
		{
			name:          "valid https URL with port",
			url:           "https://example.com:8080",
			allowInsecure: false,
			wantErr:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			parsedURL, err := validateURLScheme(tt.url, tt.allowInsecure)

			if tt.wantErr {
				assert.Error(t, err)
				if tt.errMsg != "" {
					assert.Contains(t, err.Error(), tt.errMsg)
				}
				assert.Nil(t, parsedURL)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, parsedURL)
				assert.Equal(t, tt.url, parsedURL.String())
			}
		})
	}
}

func TestMakeAbsolutePath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		path    string
		wantErr bool
	}{
		{
			name:    "relative path",
			path:    "./test.txt",
			wantErr: false,
		},
		{
			name:    "absolute path",
			path:    "/tmp/test.txt",
			wantErr: false,
		},
		{
			name:    "path with dots",
			path:    "../test.txt",
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			absPath, err := makeAbsolutePath(tt.path)

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.True(t, filepath.IsAbs(absPath), "expected absolute path, got: %s", absPath)
			}
		})
	}
}
