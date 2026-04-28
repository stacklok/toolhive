// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFormatLogLine(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		raw            string
		expectContains []string
	}{
		{
			name:           "non-JSON passthrough",
			raw:            "plain log line",
			expectContains: []string{"plain log line"},
		},
		{
			name:           "empty string",
			raw:            "",
			expectContains: []string{""},
		},
		{
			name:           "invalid JSON passthrough",
			raw:            "{not valid json",
			expectContains: []string{"{not valid json"},
		},
		{
			name:           "valid slog JSON extracts message",
			raw:            `{"time":"2025-01-15T10:30:45.123Z","level":"INFO","msg":"server started"}`,
			expectContains: []string{"10:30:45", "INFO", "server started"},
		},
		{
			name:           "extra fields included in output",
			raw:            `{"time":"2025-01-15T10:30:45.123Z","level":"ERROR","msg":"failed","component":"proxy"}`,
			expectContains: []string{"ERROR", "failed", "component=proxy"},
		},
		{
			name:           "short timestamp handled gracefully",
			raw:            `{"time":"short","level":"WARN","msg":"test"}`,
			expectContains: []string{"WARN", "test"},
		},
		{
			name:           "trailing CR stripped",
			raw:            `{"time":"2025-01-15T10:30:45.123Z","level":"DEBUG","msg":"ok"}` + "\r",
			expectContains: []string{"ok"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result := formatLogLine(tc.raw)
			for _, substr := range tc.expectContains {
				assert.Contains(t, result, substr)
			}
		})
	}
}

func TestLevelStyle(t *testing.T) {
	t.Parallel()
	levels := []string{"ERROR", "WARN", "INFO", "DEBUG", "TRACE", ""}
	for _, level := range levels {
		t.Run("level_"+level, func(t *testing.T) {
			t.Parallel()
			result := levelStyle(level)
			assert.NotEmpty(t, result, "levelStyle should return non-empty for level %q", level)
			if level != "" {
				assert.Contains(t, result, "\x1b[", "non-empty level %q should produce ANSI styled output", level)
			}
		})
	}

	// ERROR and INFO must produce different styled output.
	errorResult := levelStyle("ERROR")
	infoResult := levelStyle("INFO")
	assert.NotEqual(t, errorResult, infoResult, "ERROR and INFO should produce different styled output")
}
