// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package logger

import (
	"bytes"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive-core/env/mocks"
	"github.com/stacklok/toolhive-core/logging"
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

// setSingletonForTest temporarily replaces the singleton logger and restores
// the original when the test completes.
func setSingletonForTest(t *testing.T, l *slog.Logger) {
	t.Helper()
	prev := singleton.Load()
	singleton.Store(l)
	t.Cleanup(func() { singleton.Store(prev) })
}

// TestLogLevels tests that each log function writes to the underlying handler.
func TestLogLevels(t *testing.T) { //nolint:paralleltest // mutates singleton
	tests := []struct {
		name     string
		logFn    func()
		contains string
	}{
		{"Debug", func() { Debug("debug msg") }, "debug msg"},
		{"Debugf", func() { Debugf("debug %s", "formatted") }, "debug formatted"},
		{"Debugw", func() { Debugw("debug kv", "key", "val") }, "debug kv"},
		{"Info", func() { Info("info msg") }, "info msg"},
		{"Infof", func() { Infof("info %s", "formatted") }, "info formatted"},
		{"Infow", func() { Infow("info kv", "key", "val") }, "info kv"},
		{"Warn", func() { Warn("warn msg") }, "warn msg"},
		{"Warnf", func() { Warnf("warn %s", "formatted") }, "warn formatted"},
		{"Warnw", func() { Warnw("warn kv", "key", "val") }, "warn kv"},
		{"Error", func() { Error("error msg") }, "error msg"},
		{"Errorf", func() { Errorf("error %s", "formatted") }, "error formatted"},
		{"Errorw", func() { Errorw("error kv", "key", "val") }, "error kv"},
		{"DPanic", func() { DPanic("dpanic msg") }, "dpanic msg"},
		{"DPanicf", func() { DPanicf("dpanic %s", "formatted") }, "dpanic formatted"},
		{"DPanicw", func() { DPanicw("dpanic kv", "key", "val") }, "dpanic kv"},
	}

	for _, tc := range tests { //nolint:paralleltest // mutates singleton
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			l := logging.New(
				logging.WithOutput(&buf),
				logging.WithLevel(slog.LevelDebug),
			)
			setSingletonForTest(t, l)

			tc.logFn()

			assert.Contains(t, buf.String(), tc.contains)
		})
	}
}

// TestPanicFunctions tests that Panic/Panicf/Panicw log and panic.
func TestPanicFunctions(t *testing.T) { //nolint:paralleltest // mutates singleton
	tests := []struct {
		name     string
		logFn    func()
		contains string
	}{
		{"Panic", func() { Panic("panic msg") }, "panic msg"},
		{"Panicf", func() { Panicf("panic %s", "formatted") }, "panic formatted"},
		{"Panicw", func() { Panicw("panic kv", "key", "val") }, "panic kv"},
	}

	for _, tc := range tests { //nolint:paralleltest // mutates singleton
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			l := logging.New(
				logging.WithOutput(&buf),
				logging.WithLevel(slog.LevelDebug),
			)
			setSingletonForTest(t, l)

			require.Panics(t, func() { tc.logFn() })
			assert.Contains(t, buf.String(), tc.contains)
		})
	}
}

// TestNewLogr verifies that NewLogr returns a usable logr.Logger.
func TestNewLogr(t *testing.T) { //nolint:paralleltest // mutates singleton
	var buf bytes.Buffer
	l := logging.New(
		logging.WithOutput(&buf),
		logging.WithLevel(slog.LevelDebug),
	)
	setSingletonForTest(t, l)

	lr := NewLogr()
	lr.Info("logr test message")

	assert.Contains(t, buf.String(), "logr test message")
}

// TestGet verifies that Get returns the current singleton logger.
func TestGet(t *testing.T) { //nolint:paralleltest // mutates singleton
	var buf bytes.Buffer
	l := logging.New(logging.WithOutput(&buf))
	setSingletonForTest(t, l)

	got := Get()
	require.NotNil(t, got)

	got.Info("get test")
	assert.Contains(t, buf.String(), "get test")
}

// TestInitializeWithEnv tests Initialize with different env configurations.
func TestInitializeWithEnv(t *testing.T) { //nolint:paralleltest // mutates singleton
	tests := []struct {
		name            string
		unstructuredEnv string
	}{
		{"Default (unstructured)", ""},
		{"Explicit unstructured", "true"},
		{"Structured JSON", "false"},
	}

	for _, tc := range tests { //nolint:paralleltest // mutates singleton
		t.Run(tc.name, func(t *testing.T) {
			prev := singleton.Load()
			t.Cleanup(func() { singleton.Store(prev) })

			ctrl := gomock.NewController(t)
			mockEnv := mocks.NewMockReader(ctrl)
			mockEnv.EXPECT().Getenv("UNSTRUCTURED_LOGS").Return(tc.unstructuredEnv)

			InitializeWithEnv(mockEnv)

			got := singleton.Load()
			require.NotNil(t, got)

			// Verify the logger works by writing a message
			got.Info("test after initialize")
		})
	}
}
