// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"testing"

	"github.com/stretchr/testify/assert"

	rt "github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/core"
)

func TestWrapText(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		text     string
		maxW     int
		indent   string
		expected []string
	}{
		{
			name:     "empty string",
			text:     "",
			maxW:     40,
			indent:   "",
			expected: nil,
		},
		{
			name:     "single word shorter than maxW",
			text:     "hello",
			maxW:     40,
			indent:   "  ",
			expected: []string{"  hello"},
		},
		{
			name:     "wraps at word boundary",
			text:     "hello world foo bar",
			maxW:     12,
			indent:   "",
			expected: []string{"hello world", "foo bar"},
		},
		{
			name:     "word longer than maxW stays on its own line",
			text:     "superlongword short",
			maxW:     5,
			indent:   "",
			expected: []string{"superlongword", "short"},
		},
		{
			name:     "unicode characters counted as runes",
			text:     "\u4f60\u597d \u4e16\u754c \u6d4b\u8bd5 \u6587\u672c",
			maxW:     7,
			indent:   "",
			expected: []string{"\u4f60\u597d \u4e16\u754c", "\u6d4b\u8bd5 \u6587\u672c"},
		},
		{
			name:     "indent prefix included in width calculation",
			text:     "aaa bbb ccc",
			maxW:     8,
			indent:   ">>> ",
			expected: []string{">>> aaa", ">>> bbb", ">>> ccc"},
		},
		{
			name:     "whitespace-only input",
			text:     "   ",
			maxW:     40,
			indent:   "",
			expected: nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.expected, wrapText(tc.text, tc.maxW, tc.indent))
		})
	}
}

func TestRunesTruncate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		input    string
		n        int
		expected string
	}{
		{
			name:     "no truncation needed",
			input:    "hello",
			n:        10,
			expected: "hello",
		},
		{
			name:     "exact length",
			input:    "hello",
			n:        5,
			expected: "hello",
		},
		{
			name:     "truncated with ellipsis",
			input:    "hello world",
			n:        5,
			expected: "hell\u2026",
		},
		{
			name:     "unicode input truncated",
			input:    "\u4f60\u597d\u4e16\u754c\u6d4b\u8bd5",
			n:        3,
			expected: "\u4f60\u597d\u2026",
		},
		{
			name:     "n equals 1 gives just ellipsis",
			input:    "hello",
			n:        1,
			expected: "\u2026",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.expected, runesTruncate(tc.input, tc.n))
		})
	}
}

func TestTruncateSidebar(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		input    string
		n        int
		expected string
	}{
		{
			name:     "n <= 0 returns original",
			input:    "hello",
			n:        0,
			expected: "hello",
		},
		{
			name:     "negative n returns original",
			input:    "hello",
			n:        -5,
			expected: "hello",
		},
		{
			name:     "exact length no truncation",
			input:    "hello",
			n:        5,
			expected: "hello",
		},
		{
			name:     "truncated with ellipsis",
			input:    "hello world",
			n:        5,
			expected: "hell\u2026",
		},
		{
			name:     "short string not truncated",
			input:    "hi",
			n:        10,
			expected: "hi",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.expected, truncateSidebar(tc.input, tc.n))
		})
	}
}

func TestCountStatuses(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		workloads       []core.Workload
		expectedRunning int
		expectedStopped int
	}{
		{
			name:            "empty list",
			workloads:       nil,
			expectedRunning: 0,
			expectedStopped: 0,
		},
		{
			name: "all running variants counted as running",
			workloads: []core.Workload{
				{Status: rt.WorkloadStatusRunning},
				{Status: rt.WorkloadStatusUnauthenticated},
				{Status: rt.WorkloadStatusUnhealthy},
			},
			expectedRunning: 3,
			expectedStopped: 0,
		},
		{
			name: "all stopped variants counted as stopped",
			workloads: []core.Workload{
				{Status: rt.WorkloadStatusStopped},
				{Status: rt.WorkloadStatusError},
				{Status: rt.WorkloadStatusStarting},
				{Status: rt.WorkloadStatusStopping},
				{Status: rt.WorkloadStatusRemoving},
				{Status: rt.WorkloadStatusUnknown},
			},
			expectedRunning: 0,
			expectedStopped: 6,
		},
		{
			name: "mixed statuses",
			workloads: []core.Workload{
				{Status: rt.WorkloadStatusRunning},
				{Status: rt.WorkloadStatusStopped},
				{Status: rt.WorkloadStatusRunning},
				{Status: rt.WorkloadStatusError},
			},
			expectedRunning: 2,
			expectedStopped: 2,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			running, stopped := countStatuses(tc.workloads)
			assert.Equal(t, tc.expectedRunning, running)
			assert.Equal(t, tc.expectedStopped, stopped)
		})
	}
}
