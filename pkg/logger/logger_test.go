package logger

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"strings"
	"testing"
)

// TestUnstructuredLogs tests the unstructuredLogs function
func TestUnstructuredLogs(t *testing.T) {
	tests := []struct {
		name     string
		envValue string
		expected bool
	}{
		{"Default Case", "", true},
		{"Explicitly True", "true", true},
		{"Explicitly False", "false", false},
		{"Invalid Value", "not-a-bool", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set environment variable
			if tt.envValue != "" {
				os.Setenv("UNSTRUCTURED_LOGS", tt.envValue)
				defer os.Unsetenv("UNSTRUCTURED_LOGS")
			} else {
				os.Unsetenv("UNSTRUCTURED_LOGS")
			}

			if got := unstructuredLogs(); got != tt.expected {
				t.Errorf("unstructuredLogs() = %v, want %v", got, tt.expected)
			}
		})
	}
}

// mockWriter is a simple io.Writer implementation for testing
type mockWriter struct {
	buf bytes.Buffer
}

func (w *mockWriter) Write(p []byte) (n int, err error) {
	return w.buf.Write(p)
}

func (w *mockWriter) String() string {
	return w.buf.String()
}

func (w *mockWriter) Reset() {
	w.buf.Reset()
}

// TestSlogLogger tests the slogLogger implementation
func TestSlogLogger(t *testing.T) {
	// Create a buffer to capture log output
	var buf mockWriter

	// Create a logger that writes to our buffer
	handler := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	logger := &slogLogger{logger: slog.New(handler)}

	// Test all log levels
	testCases := []struct {
		method   string
		logFunc  func(msg string, args ...any)
		level    string
		message  string
		key      string
		value    string
		contains bool
	}{
		{"Debug", logger.Debug, "DEBUG", "debug message", "key", "value", true},
		{"Info", logger.Info, "INFO", "info message", "key", "value", true},
		{"Warn", logger.Warn, "WARN", "warn message", "key", "value", true},
		{"Error", logger.Error, "ERROR", "error message", "key", "value", true},
	}

	for _, tc := range testCases {
		t.Run(tc.method, func(t *testing.T) {
			buf.Reset()
			tc.logFunc(tc.message, tc.key, tc.value)

			output := buf.String()

			// Parse JSON output
			var logEntry map[string]interface{}
			if err := json.Unmarshal([]byte(output), &logEntry); err != nil {
				t.Fatalf("Failed to parse JSON log output: %v", err)
			}

			// Check level
			if level, ok := logEntry["level"].(string); !ok || level != tc.level {
				t.Errorf("Expected level %s, got %v", tc.level, logEntry["level"])
			}

			// Check message
			if msg, ok := logEntry["msg"].(string); !ok || msg != tc.message {
				t.Errorf("Expected message %s, got %v", tc.message, logEntry["msg"])
			}

			// Check key-value pair
			if value, ok := logEntry[tc.key].(string); !ok || value != tc.value {
				t.Errorf("Expected %s=%s, got %v", tc.key, tc.value, logEntry[tc.key])
			}
		})
	}
}

// TestInitialize tests the Initialize function
func TestInitialize(t *testing.T) {
	// Test structured logs (JSON)
	t.Run("Structured Logs", func(t *testing.T) {
		// Set environment to use structured logs
		os.Setenv("UNSTRUCTURED_LOGS", "false")
		defer os.Unsetenv("UNSTRUCTURED_LOGS")

		// Redirect stdout to capture output
		oldStdout := os.Stdout
		r, w, _ := os.Pipe()
		os.Stdout = w

		// Run initialization
		Initialize()

		// Log a test message
		Log.Info("test message", "key", "value")

		// Restore stdout
		w.Close()
		os.Stdout = oldStdout

		// Read captured output
		var buf bytes.Buffer
		buf.ReadFrom(r)
		output := buf.String()

		// Verify JSON format
		var logEntry map[string]interface{}
		if err := json.Unmarshal([]byte(output), &logEntry); err != nil {
			t.Fatalf("Failed to parse JSON log output: %v", err)
		}

		if msg, ok := logEntry["msg"].(string); !ok || msg != "test message" {
			t.Errorf("Expected message 'test message', got %v", logEntry["msg"])
		}
	})

	// Test unstructured logs
	t.Run("Unstructured Logs", func(t *testing.T) {
		// Set environment to use unstructured logs
		os.Setenv("UNSTRUCTURED_LOGS", "true")
		defer os.Unsetenv("UNSTRUCTURED_LOGS")

		// Redirect stderr to capture output
		oldStderr := os.Stderr
		r, w, _ := os.Pipe()
		os.Stderr = w

		// Run initialization
		Initialize()

		// Log a test message
		Log.Info("test message", "key", "value")

		// Restore stderr
		w.Close()
		os.Stderr = oldStderr

		// Read captured output
		var buf bytes.Buffer
		buf.ReadFrom(r)
		output := buf.String()

		// Verify unstructured format (should contain message but not be JSON)
		if !strings.Contains(output, "test message") {
			t.Errorf("Expected output to contain 'test message', got %s", output)
		}

		if !strings.Contains(output, "INF") {
			t.Errorf("Expected output to contain 'INF', got %s", output)
		}
	})
}

// TestGetLogger tests the GetLogger function
func TestGetLogger(t *testing.T) {
	// Set up structured logger for testing
	os.Setenv("UNSTRUCTURED_LOGS", "false")
	defer os.Unsetenv("UNSTRUCTURED_LOGS")

	// Redirect stdout to capture output
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	// Initialize and get a component logger
	Initialize()
	componentLogger := GetLogger("test-component")

	// Log a test message
	componentLogger.Info("component message")

	// Restore stdout
	w.Close()
	os.Stdout = oldStdout

	// Read captured output
	var buf bytes.Buffer
	buf.ReadFrom(r)
	output := buf.String()

	// Parse JSON output
	var logEntry map[string]interface{}
	if err := json.Unmarshal([]byte(output), &logEntry); err != nil {
		t.Fatalf("Failed to parse JSON log output: %v", err)
	}

	// Verify the component was added
	if component, ok := logEntry["component"].(string); !ok || component != "test-component" {
		t.Errorf("Expected component='test-component', got %v", logEntry["component"])
	}
}
