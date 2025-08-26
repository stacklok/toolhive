package logger

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/env/mocks"
)

// TestUnstructuredLogsCheck tests the unstructuredLogs function
func TestUnstructuredLogsCheck(t *testing.T) {
	t.Parallel()

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
			t.Parallel()
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockEnv := mocks.NewMockReader(ctrl)
			mockEnv.EXPECT().Getenv("UNSTRUCTURED_LOGS").Return(tt.envValue)

			if got := unstructuredLogsWithEnv(mockEnv); got != tt.expected {
				t.Errorf("unstructuredLogsWithEnv() = %v, want %v", got, tt.expected)
			}
		})
	}
}

// TestStructuredLogger tests the structured logger functionality
// TODO: Keeping this for migration but can be removed as we don't need really need to test zap
func TestStructuredLogger(t *testing.T) { //nolint:paralleltest // Uses global logger state and output capture
	const (
		levelDebug  = "debug"
		levelInfo   = "info"
		levelWarn   = "warn"
		levelError  = "error"
		levelDPanic = "dpanic"
		levelPanic  = "panic"
	)
	// Test cases for basic logging methods (Debug, Info, Warn, etc.)
	basicLogTestCases := []struct {
		level   string // The log level to test
		message string // The message to log
	}{
		{levelDebug, "debug message"},
		{levelInfo, "info message"},
		{levelWarn, "warn message"},
		{levelError, "error message"},
		{levelDPanic, "dpanic message"},
		{levelPanic, "panic message"},
	}

	for _, tc := range basicLogTestCases {
		t.Run("BasicLogs_"+tc.level, func(t *testing.T) { //nolint:paralleltest // Uses global logger state and output capture
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			// we create a pipe to capture the output of the log
			originalStdout := os.Stdout
			r, w, _ := os.Pipe()
			os.Stdout = w

			mockEnv := mocks.NewMockReader(ctrl)
			mockEnv.EXPECT().Getenv("UNSTRUCTURED_LOGS").Return("false")

			viper.SetDefault("debug", true)

			InitializeWithEnv(mockEnv)

			// Handle panic and fatal recovery
			defer func() {
				if r := recover(); r != nil {
					if tc.level != levelPanic && tc.level != levelDPanic {
						t.Errorf("Unexpected panic for level %s: %v", tc.level, r)
					}
				}
			}()

			// Log using basic methods
			switch tc.level {
			case levelDebug:
				Debug(tc.message)
			case levelInfo:
				Info(tc.message)
			case levelWarn:
				Warn(tc.message)
			case levelError:
				Error(tc.message)
			case levelDPanic:
				DPanic(tc.message)
			case levelPanic:
				Panic(tc.message)
			}

			w.Close()
			os.Stdout = originalStdout

			// Read the captured output
			var capturedOutput bytes.Buffer
			io.Copy(&capturedOutput, r)
			output := capturedOutput.String()

			// Parse JSON output
			var logEntry map[string]any
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
		})
	}

	// Test cases for structured logging methods (Debugw, Infow, etc.)
	structuredLogTestCases := []struct {
		level   string // The log level to test
		message string // The message to log
		key     string // Key for structured logging
		value   string // Value for structured logging
	}{
		{levelDebug, "debug message", "key", "value"},
		{levelInfo, "info message", "key", "value"},
		{levelWarn, "warn message", "key", "value"},
		{levelError, "error message", "key", "value"},
		{levelDPanic, "dpanic message", "key", "value"},
		{levelPanic, "panic message", "key", "value"},
	}

	for _, tc := range structuredLogTestCases {
		t.Run("StructuredLogs_"+tc.level, func(t *testing.T) { //nolint:paralleltest // Uses global logger state and output capture
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			// we create a pipe to capture the output of the log
			originalStdout := os.Stdout
			r, w, _ := os.Pipe()
			os.Stdout = w

			mockEnv := mocks.NewMockReader(ctrl)
			mockEnv.EXPECT().Getenv("UNSTRUCTURED_LOGS").Return("false")

			viper.SetDefault("debug", true)

			InitializeWithEnv(mockEnv)

			// Handle panic and fatal recovery
			defer func() {
				if r := recover(); r != nil {
					if tc.level != "panic" && tc.level != levelDPanic {
						t.Errorf("Unexpected panic for level %s: %v", tc.level, r)
					}
				}
			}()

			// Log using structured methods
			switch tc.level {
			case levelDebug:
				Debugw(tc.message, tc.key, tc.value)
			case levelInfo:
				Infow(tc.message, tc.key, tc.value)
			case levelWarn:
				Warnw(tc.message, tc.key, tc.value)
			case levelError:
				Errorw(tc.message, tc.key, tc.value)
			case levelDPanic:
				DPanicw(tc.message, tc.key, tc.value)
			case levelPanic:
				Panicw(tc.message, tc.key, tc.value)
			}

			w.Close()
			os.Stdout = originalStdout

			// Read the captured output
			var capturedOutput bytes.Buffer
			io.Copy(&capturedOutput, r)
			output := capturedOutput.String()

			// Parse JSON output
			var logEntry map[string]any
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

	// Test cases for formatted logging methods (Debugf, Infof, etc.)
	formattedLogTestCases := []struct {
		level    string
		message  string
		key      string
		value    string
		expected string
		contains bool
	}{
		{levelDebug, "debug message %s and %s", "key", "value", "debug message key and value", true},
		{levelInfo, "info message %s and %s", "key", "value", "info message key and value", true},
		{levelWarn, "warn message %s and %s", "key", "value", "warn message key and value", true},
		{levelError, "error message %s and %s", "key", "value", "error message key and value", true},
		{levelDPanic, "dpanic message %s and %s", "key", "value", "dpanic message key and value", true},
		{levelPanic, "panic message %s and %s", "key", "value", "panic message key and value", true},
	}

	for _, tc := range formattedLogTestCases { //nolint:paralleltest // Uses global logger state and output capture
		t.Run("FormattedLogs_"+tc.level, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			// we create a pipe to capture the output of the log
			originalStdout := os.Stdout
			r, w, _ := os.Pipe()
			os.Stdout = w

			mockEnv := mocks.NewMockReader(ctrl)
			mockEnv.EXPECT().Getenv("UNSTRUCTURED_LOGS").Return("false")

			viper.SetDefault("debug", true)

			InitializeWithEnv(mockEnv)

			// Handle panic and fatal recovery
			defer func() {
				if r := recover(); r != nil {
					if tc.level != levelPanic && tc.level != levelDPanic {
						t.Errorf("Unexpected panic for level %s: %v", tc.level, r)
					}
				}
			}()

			// Log using formatted methods
			switch tc.level {
			case levelDebug:
				Debugf(tc.message, tc.key, tc.value)
			case levelInfo:
				Infof(tc.message, tc.key, tc.value)
			case levelWarn:
				Warnf(tc.message, tc.key, tc.value)
			case levelError:
				Errorf(tc.message, tc.key, tc.value)
			case levelDPanic:
				DPanicf(tc.message, tc.key, tc.value)
			case levelPanic:
				Panicf(tc.message, tc.key, tc.value)
			}
			w.Close()
			os.Stdout = originalStdout

			// Read the captured output
			var capturedOutput bytes.Buffer
			io.Copy(&capturedOutput, r)
			output := capturedOutput.String()

			// Parse JSON output
			var logEntry map[string]any
			if err := json.Unmarshal([]byte(output), &logEntry); err != nil {
				t.Fatalf("Failed to parse JSON log output: %v", err)
			}

			// Check level
			if level, ok := logEntry["level"].(string); !ok || level != tc.level {
				t.Errorf("Expected level %s, got %v", tc.level, logEntry["level"])
			}

			// Check message
			if msg, ok := logEntry["msg"].(string); !ok || msg != tc.expected {
				t.Errorf("Expected message %s, got %v", tc.expected, logEntry["msg"])
			}
		})
	}
}

// TestUnstructuredLogger tests the unstructured logger functionality
func TestUnstructuredLogger(t *testing.T) { //nolint:paralleltest // Uses global logger state and output capture
	// we only test for the formatted logs here because the unstructured logs
	// do not contain the key/value pair format that the structured logs do
	const (
		levelDebug  = "DEBUG"
		levelInfo   = "INFO"
		levelWarn   = "WARN"
		levelError  = "ERROR"
		levelDPanic = "DPANIC"
		levelPanic  = "PANIC"
	)

	formattedLogTestCases := []struct {
		level    string
		message  string
		key      string
		value    string
		expected string
	}{
		{levelDebug, "debug message %s and %s", "key", "value", "debug message key and value"},
		{levelInfo, "info message %s and %s", "key", "value", "info message key and value"},
		{levelWarn, "warn message %s and %s", "key", "value", "warn message key and value"},
		{levelError, "error message %s and %s", "key", "value", "error message key and value"},
		{levelDPanic, "dpanic message %s and %s", "key", "value", "dpanic message key and value"},
		{levelPanic, "panic message %s and %s", "key", "value", "panic message key and value"},
	}

	for _, tc := range formattedLogTestCases { //nolint:paralleltest // Uses global logger state and output capture
		t.Run("FormattedLogs_"+tc.level, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			// we create a pipe to capture the output of the log
			// so we can test that the logger logs the right message
			originalStderr := os.Stderr
			r, w, _ := os.Pipe()
			os.Stderr = w

			mockEnv := mocks.NewMockReader(ctrl)
			mockEnv.EXPECT().Getenv("UNSTRUCTURED_LOGS").Return("true")

			viper.SetDefault("debug", true)

			InitializeWithEnv(mockEnv)

			// Handle panic recovery for DPANIC and PANIC levels
			defer func() {
				if r := recover(); r != nil {
					// Expected for panic levels
					if tc.level != "PANIC" && tc.level != "DPANIC" {
						t.Errorf("Unexpected panic for level %s: %v", tc.level, r)
					}
				}
			}()

			// Log the message based on the level
			switch tc.level {
			case levelDebug:
				Debugf(tc.message, tc.key, tc.value)
			case levelInfo:
				Infof(tc.message, tc.key, tc.value)
			case levelWarn:
				Warnf(tc.message, tc.key, tc.value)
			case levelError:
				Errorf(tc.message, tc.key, tc.value)
			case levelDPanic:
				DPanicf(tc.message, tc.key, tc.value)
			case levelPanic:
				Panicf(tc.message, tc.key, tc.value)
			}

			w.Close()
			os.Stderr = originalStderr

			// Read the captured output
			var capturedOutput bytes.Buffer
			io.Copy(&capturedOutput, r)
			output := capturedOutput.String()

			assert.Contains(t, output, tc.level, "Expected log entry '%s' to contain log level '%s'", output, tc.level)
			assert.Contains(t, output, tc.expected, "Expected log entry '%s' to contain message '%s'", output, tc.expected)
		})
	}
}

// TestInitialize tests the Initialize function
func TestInitialize(t *testing.T) { //nolint:paralleltest // Uses global logger state and output capture
	// Test structured logs (JSON)
	t.Run("Structured Logs", func(t *testing.T) { //nolint:paralleltest // Uses global logger state and output capture
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockEnv := mocks.NewMockReader(ctrl)
		mockEnv.EXPECT().Getenv("UNSTRUCTURED_LOGS").Return("false")

		// Redirect stdout to capture output
		oldStdout := os.Stdout
		r, w, _ := os.Pipe()
		os.Stdout = w

		// Run initialization
		InitializeWithEnv(mockEnv)

		// Log a test message
		Info("test message")

		// Restore stdout
		w.Close()
		os.Stdout = oldStdout

		// Read captured output
		var buf bytes.Buffer
		buf.ReadFrom(r)
		output := buf.String()

		// Verify JSON format
		var logEntry map[string]any
		if err := json.Unmarshal([]byte(output), &logEntry); err != nil {
			t.Fatalf("Failed to parse JSON log output: %v", err)
		}

		if msg, ok := logEntry["msg"].(string); !ok || msg != "test message" {
			t.Errorf("Expected message 'test message', got %v", logEntry["msg"])
		}
	})

	// Test unstructured logs
	t.Run("Unstructured Logs", func(t *testing.T) { //nolint:paralleltest // Uses global logger state and output capture
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockEnv := mocks.NewMockReader(ctrl)
		mockEnv.EXPECT().Getenv("UNSTRUCTURED_LOGS").Return("true")

		// Redirect stderr to capture output
		oldStderr := os.Stderr
		r, w, _ := os.Pipe()
		os.Stderr = w

		// Run initialization
		InitializeWithEnv(mockEnv)

		// Log a test message
		Info("test message")

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
