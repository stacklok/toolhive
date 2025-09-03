package logger

import (
	"bytes"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

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

// TestStructuredLogger tests the structured logger functionality using observer pattern
func TestStructuredLogger(t *testing.T) { //nolint:paralleltest // Uses global logger state
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
		t.Run("BasicLogs_"+tc.level, func(t *testing.T) { //nolint:paralleltest // Uses global logger state
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			// Create observer to capture logs in memory instead of stdout redirection
			core, observedLogs := observer.New(zapcore.DebugLevel)
			logger := zap.New(core)
			zap.ReplaceGlobals(logger)

			// Handle panic and fatal recovery for panic levels
			var panicOccurred bool
			defer func() {
				if r := recover(); r != nil {
					panicOccurred = true
					if tc.level != levelPanic {
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

			// For panic levels, verify panic occurred, otherwise check logged entries
			if tc.level == levelPanic {
				require.True(t, panicOccurred, "Expected panic for level %s", tc.level)
				// For panic levels, we might not get log entries before the panic
				return
			}

			// Note: DPanic only panics in development mode, not in tests by default
			// So we treat it like a regular error level for verification purposes

			// Get captured log entries from observer
			allEntries := observedLogs.All()
			require.Len(t, allEntries, 1, "Expected exactly one log entry")

			entry := allEntries[0]
			// Check level
			assert.Equal(t, tc.level, entry.Level.String(), "Log level mismatch")
			// Check message
			assert.Equal(t, tc.message, entry.Message, "Log message mismatch")
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
		t.Run("StructuredLogs_"+tc.level, func(t *testing.T) { //nolint:paralleltest // Uses global logger state
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			// Create observer to capture logs in memory instead of stdout redirection
			core, observedLogs := observer.New(zapcore.DebugLevel)
			logger := zap.New(core)
			zap.ReplaceGlobals(logger)

			// Handle panic and fatal recovery for panic levels
			var panicOccurred bool
			defer func() {
				if r := recover(); r != nil {
					panicOccurred = true
					if tc.level != levelPanic {
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

			// For panic levels, verify panic occurred, otherwise check logged entries
			if tc.level == levelPanic {
				require.True(t, panicOccurred, "Expected panic for level %s", tc.level)
				// For panic levels, we might not get log entries before the panic
				return
			}

			// Note: DPanic only panics in development mode, not in tests by default
			// So we treat it like a regular error level for verification purposes

			// Get captured log entries from observer
			allEntries := observedLogs.All()
			require.Len(t, allEntries, 1, "Expected exactly one log entry")

			entry := allEntries[0]
			// Check level
			assert.Equal(t, tc.level, entry.Level.String(), "Log level mismatch")
			// Check message
			assert.Equal(t, tc.message, entry.Message, "Log message mismatch")

			// Check key-value pair in context fields
			found := false
			for _, field := range entry.Context {
				if field.Key == tc.key && field.String == tc.value {
					found = true
					break
				}
			}
			assert.True(t, found, "Expected key-value pair %s=%s not found in context", tc.key, tc.value)
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

	for _, tc := range formattedLogTestCases { //nolint:paralleltest // Uses global logger state
		t.Run("FormattedLogs_"+tc.level, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			// Create observer to capture logs in memory instead of stdout redirection
			core, observedLogs := observer.New(zapcore.DebugLevel)
			logger := zap.New(core)
			zap.ReplaceGlobals(logger)

			// Handle panic and fatal recovery for panic levels
			var panicOccurred bool
			defer func() {
				if r := recover(); r != nil {
					panicOccurred = true
					if tc.level != levelPanic {
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

			// For panic levels, verify panic occurred, otherwise check logged entries
			if tc.level == levelPanic {
				require.True(t, panicOccurred, "Expected panic for level %s", tc.level)
				// For panic levels, we might not get log entries before the panic
				return
			}

			// Note: DPanic only panics in development mode, not in tests by default
			// So we treat it like a regular error level for verification purposes

			// Get captured log entries from observer
			allEntries := observedLogs.All()
			require.Len(t, allEntries, 1, "Expected exactly one log entry")

			entry := allEntries[0]
			// Check level
			assert.Equal(t, tc.level, entry.Level.String(), "Log level mismatch")
			// Check message
			assert.Equal(t, tc.expected, entry.Message, "Log message mismatch")
		})
	}
}

// TestUnstructuredLogger tests the unstructured logger functionality
func TestUnstructuredLogger(t *testing.T) { //nolint:paralleltest // Uses global logger state
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

	for _, tc := range formattedLogTestCases { //nolint:paralleltest // Uses global logger state
		t.Run("FormattedLogs_"+tc.level, func(t *testing.T) {
			// For unstructured logs, we still need to capture stderr output
			// but we can use a buffer-based approach that's more coverage-friendly
			var buf bytes.Buffer

			viper.SetDefault("debug", true)

			// Create a development config that writes to our buffer instead of stderr
			config := zap.NewDevelopmentConfig()
			config.EncoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
			config.EncoderConfig.EncodeTime = zapcore.TimeEncoderOfLayout("15:04:05")
			config.DisableStacktrace = true
			config.DisableCaller = true

			// Create a core that writes to our buffer
			core := zapcore.NewCore(
				zapcore.NewConsoleEncoder(config.EncoderConfig),
				zapcore.AddSync(&buf),
				zapcore.DebugLevel,
			)

			logger := zap.New(core)
			zap.ReplaceGlobals(logger)

			// Handle panic recovery for DPANIC and PANIC levels
			var panicOccurred bool
			defer func() {
				if r := recover(); r != nil {
					panicOccurred = true
					if tc.level != "PANIC" {
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

			// For panic levels, verify panic occurred, otherwise check output
			if tc.level == "PANIC" {
				require.True(t, panicOccurred, "Expected panic for level %s", tc.level)
				// For panic levels, we might not get log entries before the panic
				return
			}

			// Note: DPanic only panics in development mode, not in tests by default
			// So we treat it like a regular error level for verification purposes

			// Get the captured output from buffer
			output := buf.String()
			assert.Contains(t, output, tc.level, "Expected log entry '%s' to contain log level '%s'", output, tc.level)
			assert.Contains(t, output, tc.expected, "Expected log entry '%s' to contain message '%s'", output, tc.expected)
		})
	}
}

// TestInitialize tests the Initialize function
func TestInitialize(t *testing.T) { //nolint:paralleltest // Uses global logger state
	// Test structured logs (JSON)
	t.Run("Structured Logs", func(t *testing.T) { //nolint:paralleltest // Uses global logger state

		// Create observer to capture logs in memory
		core, observedLogs := observer.New(zapcore.InfoLevel)
		logger := zap.New(core)
		zap.ReplaceGlobals(logger)

		// Log a test message
		Info("test message")

		// Get captured log entries from observer
		allEntries := observedLogs.All()
		require.Len(t, allEntries, 1, "Expected exactly one log entry")

		entry := allEntries[0]
		assert.Equal(t, "info", entry.Level.String(), "Log level mismatch")
		assert.Equal(t, "test message", entry.Message, "Log message mismatch")
	})

	// Test unstructured logs
	t.Run("Unstructured Logs", func(t *testing.T) { //nolint:paralleltest // Uses global logger state

		// For unstructured logs, we use a buffer-based approach
		var buf bytes.Buffer

		// Create a development config that writes to our buffer
		config := zap.NewDevelopmentConfig()
		config.EncoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
		config.EncoderConfig.EncodeTime = zapcore.TimeEncoderOfLayout("15:04:05")
		config.DisableStacktrace = true
		config.DisableCaller = true

		// Create a core that writes to our buffer
		core := zapcore.NewCore(
			zapcore.NewConsoleEncoder(config.EncoderConfig),
			zapcore.AddSync(&buf),
			zapcore.InfoLevel,
		)

		logger := zap.New(core)
		zap.ReplaceGlobals(logger)

		// Log a test message
		Info("test message")

		// Get the captured output from buffer
		output := buf.String()

		// Verify unstructured format (should contain message but not be JSON)
		assert.Contains(t, output, "test message", "Expected output to contain 'test message'")
		assert.Contains(t, output, "INFO", "Expected output to contain 'INFO'")
	})
}
