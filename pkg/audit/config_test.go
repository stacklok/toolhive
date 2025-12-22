package audit

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultConfig(t *testing.T) {
	t.Parallel()
	config := DefaultConfig()

	assert.False(t, config.IncludeRequestData)
	assert.False(t, config.IncludeResponseData)
	assert.Equal(t, 1024, config.MaxDataSize)
	assert.Empty(t, config.Component)
	assert.Empty(t, config.EventTypes)
	assert.Empty(t, config.ExcludeEventTypes)
}

func TestLoadFromReader(t *testing.T) {
	t.Parallel()
	jsonConfig := `{
		"component": "test-component",
		"event_types": ["mcp_tool_call", "mcp_resource_read"],
		"exclude_event_types": ["mcp_ping"],
		"include_request_data": true,
		"include_response_data": false,
		"max_data_size": 2048
	}`

	config, err := LoadFromReader(strings.NewReader(jsonConfig))
	require.NoError(t, err)

	assert.Equal(t, "test-component", config.Component)
	assert.Equal(t, []string{"mcp_tool_call", "mcp_resource_read"}, config.EventTypes)
	assert.Equal(t, []string{"mcp_ping"}, config.ExcludeEventTypes)
	assert.True(t, config.IncludeRequestData)
	assert.False(t, config.IncludeResponseData)
	assert.Equal(t, 2048, config.MaxDataSize)
}

func TestLoadFromReaderInvalidJSON(t *testing.T) {
	t.Parallel()
	invalidJSON := `{"invalid": }`

	_, err := LoadFromReader(strings.NewReader(invalidJSON))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to decode audit config")
}

func TestShouldAuditEventAllEventsAllowed(t *testing.T) {
	t.Parallel()
	config := &Config{}

	result := config.ShouldAuditEvent("any_event")
	assert.True(t, result)
}

func TestShouldAuditEventAllEventsEnabled(t *testing.T) {
	t.Parallel()
	config := &Config{
		// No EventTypes specified, so all events should be audited
	}

	assert.True(t, config.ShouldAuditEvent("mcp_tool_call"))
	assert.True(t, config.ShouldAuditEvent("mcp_resource_read"))
	assert.True(t, config.ShouldAuditEvent("custom_event"))
}

func TestShouldAuditEventSpecificTypes(t *testing.T) {
	t.Parallel()
	config := &Config{
		EventTypes: []string{"mcp_tool_call", "mcp_resource_read"},
	}

	assert.True(t, config.ShouldAuditEvent("mcp_tool_call"))
	assert.True(t, config.ShouldAuditEvent("mcp_resource_read"))
	assert.False(t, config.ShouldAuditEvent("mcp_ping"))
	assert.False(t, config.ShouldAuditEvent("custom_event"))
}

func TestShouldAuditEventExcludeTypes(t *testing.T) {
	t.Parallel()
	config := &Config{
		ExcludeEventTypes: []string{"mcp_ping", "mcp_logging"},
	}

	assert.True(t, config.ShouldAuditEvent("mcp_tool_call"))
	assert.True(t, config.ShouldAuditEvent("mcp_resource_read"))
	assert.False(t, config.ShouldAuditEvent("mcp_ping"))
	assert.False(t, config.ShouldAuditEvent("mcp_logging"))
}

func TestShouldAuditEventExcludeTakesPrecedence(t *testing.T) {
	t.Parallel()
	config := &Config{
		EventTypes:        []string{"mcp_tool_call", "mcp_ping"},
		ExcludeEventTypes: []string{"mcp_ping"},
	}

	assert.True(t, config.ShouldAuditEvent("mcp_tool_call"))
	assert.False(t, config.ShouldAuditEvent("mcp_ping"))          // Excluded despite being in EventTypes
	assert.False(t, config.ShouldAuditEvent("mcp_resource_read")) // Not in EventTypes
}

func TestCreateMiddleware(t *testing.T) {
	t.Parallel()
	config := &Config{}

	middleware, err := config.CreateMiddlewareWithTransport("sse")
	assert.NoError(t, err)
	assert.NotNil(t, middleware)
}

func TestValidateValidConfig(t *testing.T) {
	t.Parallel()
	config := &Config{
		EventTypes:          []string{EventTypeMCPToolCall, EventTypeMCPResourceRead},
		ExcludeEventTypes:   []string{EventTypeMCPPing},
		IncludeRequestData:  true,
		IncludeResponseData: false,
		MaxDataSize:         2048,
	}

	err := config.Validate()
	assert.NoError(t, err)
	assert.Equal(t, 2048, config.MaxDataSize, "MaxDataSize should be preserved when explicitly set")
}

func TestValidateNegativeMaxDataSize(t *testing.T) {
	t.Parallel()
	config := &Config{
		MaxDataSize: -1,
	}

	err := config.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "max_data_size cannot be negative")
}

func TestValidateAppliesDefaultMaxDataSize(t *testing.T) {
	t.Parallel()
	config := &Config{
		MaxDataSize: 0, // Not set - should become default (1024) after validation
	}

	err := config.Validate()
	assert.NoError(t, err)
	assert.Equal(t, DefaultConfig().MaxDataSize, config.MaxDataSize,
		"Validate() should apply default MaxDataSize when 0")
}

func TestValidateInvalidEventType(t *testing.T) {
	t.Parallel()
	config := &Config{
		EventTypes: []string{"invalid_event_type"},
	}

	err := config.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unknown event type: invalid_event_type")
}

func TestValidateInvalidExcludeEventType(t *testing.T) {
	t.Parallel()
	config := &Config{
		ExcludeEventTypes: []string{"invalid_exclude_type"},
	}

	err := config.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unknown exclude event type: invalid_exclude_type")
}

func TestValidateAllValidEventTypes(t *testing.T) {
	t.Parallel()
	validEventTypes := []string{
		EventTypeMCPInitialize,
		EventTypeMCPToolCall,
		EventTypeMCPToolsList,
		EventTypeMCPResourceRead,
		EventTypeMCPResourcesList,
		EventTypeMCPPromptGet,
		EventTypeMCPPromptsList,
		EventTypeMCPNotification,
		EventTypeMCPPing,
		EventTypeMCPLogging,
		EventTypeMCPCompletion,
		EventTypeMCPRootsListChanged,
	}

	config := &Config{
		EventTypes: validEventTypes,
	}

	err := config.Validate()
	assert.NoError(t, err)
}

func TestConfigJSONSerialization(t *testing.T) {
	t.Parallel()
	originalConfig := &Config{
		Component:           "test-service",
		EventTypes:          []string{EventTypeMCPToolCall, EventTypeMCPResourceRead},
		ExcludeEventTypes:   []string{EventTypeMCPPing},
		IncludeRequestData:  true,
		IncludeResponseData: false,
		MaxDataSize:         4096,
	}

	// Serialize to JSON
	jsonData, err := json.Marshal(originalConfig)
	require.NoError(t, err)

	// Deserialize back
	var deserializedConfig Config
	err = json.Unmarshal(jsonData, &deserializedConfig)
	require.NoError(t, err)

	// Verify all fields are preserved
	assert.Equal(t, originalConfig.Component, deserializedConfig.Component)
	assert.Equal(t, originalConfig.EventTypes, deserializedConfig.EventTypes)
	assert.Equal(t, originalConfig.ExcludeEventTypes, deserializedConfig.ExcludeEventTypes)
	assert.Equal(t, originalConfig.IncludeRequestData, deserializedConfig.IncludeRequestData)
	assert.Equal(t, originalConfig.IncludeResponseData, deserializedConfig.IncludeResponseData)
	assert.Equal(t, originalConfig.MaxDataSize, deserializedConfig.MaxDataSize)
}

func TestConfigMinimalJSON(t *testing.T) {
	t.Parallel()
	minimalJSON := `{}`

	config, err := LoadFromReader(strings.NewReader(minimalJSON))
	require.NoError(t, err)

	assert.Empty(t, config.Component)
	assert.Empty(t, config.EventTypes)
	assert.Empty(t, config.ExcludeEventTypes)
	assert.False(t, config.IncludeRequestData)
	assert.False(t, config.IncludeResponseData)
	assert.Equal(t, 0, config.MaxDataSize) // Default zero value
}

func TestGetMiddlewareFromFileError(t *testing.T) {
	t.Parallel()
	// Test with non-existent file
	_, err := GetMiddlewareFromFile("/non/existent/file.json", "sse")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to load audit config")
}

func TestLoadFromFilePathCleaning(t *testing.T) {
	t.Parallel()
	// Test that filepath.Clean is used (this is more of a smoke test)
	// We can't easily test the actual cleaning without creating files
	_, err := LoadFromFile("./non-existent-file.json")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to open audit config file")
}

func TestConfigWithEmptyEventTypes(t *testing.T) {
	t.Parallel()
	config := &Config{
		EventTypes: []string{}, // Explicitly empty
	}

	// Should audit all events when EventTypes is empty
	assert.True(t, config.ShouldAuditEvent("any_event"))
	assert.True(t, config.ShouldAuditEvent("mcp_tool_call"))
}

func TestConfigWithEmptyExcludeEventTypes(t *testing.T) {
	t.Parallel()
	config := &Config{
		ExcludeEventTypes: []string{}, // Explicitly empty
	}

	// Should audit all events when ExcludeEventTypes is empty
	assert.True(t, config.ShouldAuditEvent("any_event"))
	assert.True(t, config.ShouldAuditEvent("mcp_tool_call"))
}

func TestGetLogWriter(t *testing.T) {
	t.Parallel()

	t.Run("default to stdout", func(t *testing.T) {
		t.Parallel()
		config := &Config{}

		writer, err := config.GetLogWriter()
		assert.NoError(t, err)
		assert.Equal(t, os.Stdout, writer)
	})

	t.Run("nil config defaults to stdout", func(t *testing.T) {
		t.Parallel()
		var config *Config

		writer, err := config.GetLogWriter()
		assert.NoError(t, err)
		assert.Equal(t, os.Stdout, writer)
	})

	t.Run("empty log file defaults to stdout", func(t *testing.T) {
		t.Parallel()
		config := &Config{LogFile: ""}

		writer, err := config.GetLogWriter()
		assert.NoError(t, err)
		assert.Equal(t, os.Stdout, writer)
	})

	t.Run("invalid log file path returns error", func(t *testing.T) {
		t.Parallel()
		config := &Config{LogFile: "/invalid/path/that/does/not/exist/audit.log"}

		_, err := config.GetLogWriter()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to open audit log file")
	})
}

func TestConfigWithLogFile(t *testing.T) {
	t.Parallel()
	jsonConfig := `{
		"component": "test-component",
		"log_file": "/tmp/audit.log",
		"include_request_data": true
	}`

	config, err := LoadFromReader(strings.NewReader(jsonConfig))
	require.NoError(t, err)

	assert.Equal(t, "test-component", config.Component)
	assert.Equal(t, "/tmp/audit.log", config.LogFile)
	assert.True(t, config.IncludeRequestData)
}

func TestGetLogWriter_WithActualFile(t *testing.T) {
	t.Parallel()

	t.Run("creates file and writes audit logs", func(t *testing.T) {
		t.Parallel()

		// Create a temporary directory for this test
		tmpDir := t.TempDir()
		logFilePath := filepath.Join(tmpDir, "audit.log")

		// Create config with temp file path
		config := &Config{
			Component:           "test-component",
			LogFile:             logFilePath,
			IncludeRequestData:  true,
			IncludeResponseData: true,
		}

		// Get the writer
		writer, err := config.GetLogWriter()
		require.NoError(t, err)
		require.NotNil(t, writer)

		// Close the writer (it's a file)
		if closer, ok := writer.(io.Closer); ok {
			defer closer.Close()
		}

		// Verify file was created
		fileInfo, err := os.Stat(logFilePath)
		require.NoError(t, err)
		assert.False(t, fileInfo.IsDir())

		// Verify file permissions (0600 = owner read/write only)
		assert.Equal(t, os.FileMode(0600), fileInfo.Mode().Perm())

		// Read the file and verify it's empty (no events logged yet)
		content, err := os.ReadFile(logFilePath)
		require.NoError(t, err)
		assert.Empty(t, content)
	})

	t.Run("appends to existing file", func(t *testing.T) {
		t.Parallel()

		// Create a temporary directory for this test
		tmpDir := t.TempDir()
		logFilePath := filepath.Join(tmpDir, "audit.log")

		// Write initial content
		initialContent := "initial log entry\n"
		err := os.WriteFile(logFilePath, []byte(initialContent), 0600)
		require.NoError(t, err)

		// Create config pointing to the same file
		config := &Config{
			Component: "test-component",
			LogFile:   logFilePath,
		}

		// Get the writer (should open in append mode)
		writer, err := config.GetLogWriter()
		require.NoError(t, err)
		require.NotNil(t, writer)

		// Write additional content
		additionalContent := "appended log entry\n"
		n, err := writer.Write([]byte(additionalContent))
		require.NoError(t, err)
		assert.Equal(t, len(additionalContent), n)

		// Close the writer
		if closer, ok := writer.(io.Closer); ok {
			closer.Close()
		}

		// Read file and verify both entries exist in the correct order
		content, err := os.ReadFile(logFilePath)
		require.NoError(t, err)
		assert.Equal(t, initialContent+additionalContent, string(content))
	})

	t.Run("creates nested directories", func(t *testing.T) {
		t.Parallel()

		// Create a temporary directory for this test
		tmpDir := t.TempDir()

		// Use a nested path
		nestedPath := filepath.Join(tmpDir, "nested", "dir", "audit.log")

		// Create the parent directories
		err := os.MkdirAll(filepath.Dir(nestedPath), 0755)
		require.NoError(t, err)

		config := &Config{
			LogFile: nestedPath,
		}

		writer, err := config.GetLogWriter()
		require.NoError(t, err)
		require.NotNil(t, writer)

		// Verify file was created
		fileInfo, err := os.Stat(nestedPath)
		require.NoError(t, err)
		assert.False(t, fileInfo.IsDir())
		assert.Equal(t, os.FileMode(0600), fileInfo.Mode().Perm())

		if closer, ok := writer.(io.Closer); ok {
			closer.Close()
		}
	})
}

// waitForAuditLog polls the audit log file until content is available or timeout is reached.
// This is more reliable than a fixed sleep for async log writes.
func waitForAuditLog(t *testing.T, logFilePath string, timeout time.Duration) []byte {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		content, err := os.ReadFile(logFilePath)
		if err == nil && len(content) > 0 {
			return content
		}
		time.Sleep(50 * time.Millisecond) // Poll every 50ms
	}
	t.Fatalf("timeout waiting for audit log at %s after %v", logFilePath, timeout)
	return nil
}

func TestHTTPAuditor_WritesValidJSONToFile(t *testing.T) {
	t.Parallel()

	t.Run("writes valid JSON audit logs to file", func(t *testing.T) {
		t.Parallel()

		// Create a temporary file for audit logs
		tmpDir := t.TempDir()
		logFilePath := filepath.Join(tmpDir, "vmcp-http-audit.log")

		// Create audit config with file output (simulating vMCP configuration)
		config := &Config{
			Component:           "vmcp-server",
			LogFile:             logFilePath,
			IncludeRequestData:  true,
			IncludeResponseData: true,
			MaxDataSize:         1024, // Required for data capture
		}

		// Create HTTP auditor (used by vMCP for MCP protocol requests)
		auditor, err := NewAuditorWithTransport(config, "streamable-http")
		require.NoError(t, err)
		require.NotNil(t, auditor)
		t.Cleanup(func() {
			auditor.Close()
		})

		// Create a test HTTP request simulating an MCP tool call
		req := httptest.NewRequest("POST", "/mcp/tools/call", strings.NewReader(`{"tool":"calculator","params":{"operation":"add"}}`))
		req.Header.Set("Content-Type", "application/json")

		// Simulate the audit middleware
		rw := httptest.NewRecorder()
		handler := auditor.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, err := w.Write([]byte(`{"result":"success","value":42}`))
			require.NoError(t, err)
		}))
		handler.ServeHTTP(rw, req)

		// Wait for audit log to be written (with timeout)
		content := waitForAuditLog(t, logFilePath, 1*time.Second)
		require.NotEmpty(t, content, "audit log file should not be empty")

		// Verify it's valid JSON
		var logEntry map[string]any
		err = json.Unmarshal(content, &logEntry)
		require.NoError(t, err, "audit log should be valid JSON")

		// Verify required audit event fields
		assert.Contains(t, logEntry, "audit_id", "should have audit_id")
		assert.Contains(t, logEntry, "type", "should have type")
		assert.Contains(t, logEntry, "logged_at", "should have logged_at")
		assert.Contains(t, logEntry, "outcome", "should have outcome")
		assert.Contains(t, logEntry, "component", "should have component")
		assert.Contains(t, logEntry, "source", "should have source")
		assert.Contains(t, logEntry, "subjects", "should have subjects")
		assert.Contains(t, logEntry, "target", "should have target")
		assert.Contains(t, logEntry, "metadata", "should have metadata")

		// Verify component matches vMCP
		assert.Equal(t, "vmcp-server", logEntry["component"])

		// Verify outcome
		assert.Equal(t, "success", logEntry["outcome"])

		// Verify data field contains request and response (must be present since both are enabled)
		require.Contains(t, logEntry, "data", "audit log should have data field when request/response data is enabled")
		dataField := logEntry["data"]
		data, ok := dataField.(map[string]any)
		require.True(t, ok, "data should be a map")
		assert.Contains(t, data, "request", "data should contain request")
		assert.Contains(t, data, "response", "data should contain response")
	})

	t.Run("multiple HTTP requests create valid newline-delimited JSON", func(t *testing.T) {
		t.Parallel()

		// Create a temporary file for audit logs
		tmpDir := t.TempDir()
		logFilePath := filepath.Join(tmpDir, "vmcp-multiple-audit.log")

		// Create audit config with file output
		config := &Config{
			Component: "vmcp-server",
			LogFile:   logFilePath,
		}

		// Create HTTP auditor
		auditor, err := NewAuditorWithTransport(config, "streamable-http")
		require.NoError(t, err)
		t.Cleanup(func() {
			auditor.Close()
		})

		// Simulate multiple HTTP requests
		handler := auditor.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, err := w.Write([]byte(`{"result":"ok"}`))
			require.NoError(t, err)
		}))

		// Make 3 requests
		for i := 0; i < 3; i++ {
			req := httptest.NewRequest("POST", "/mcp/endpoint", strings.NewReader(`{"test":"data"}`))
			rw := httptest.NewRecorder()
			handler.ServeHTTP(rw, req)
		}

		// Wait for audit logs to be written (with timeout)
		content := waitForAuditLog(t, logFilePath, 1*time.Second)
		require.NotEmpty(t, content, "audit log file should not be empty")

		// Split by newlines and verify each line is valid JSON
		lines := strings.Split(strings.TrimSpace(string(content)), "\n")
		assert.Equal(t, 3, len(lines), "should have 3 log entries")

		for i, line := range lines {
			var logEntry map[string]any
			err := json.Unmarshal([]byte(line), &logEntry)
			require.NoError(t, err, "line %d should be valid JSON", i+1)
			assert.Contains(t, logEntry, "audit_id")
			assert.Contains(t, logEntry, "type")
			assert.Contains(t, logEntry, "component")
			assert.Equal(t, "vmcp-server", logEntry["component"])
		}
	})
}
