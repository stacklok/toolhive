// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSplitEventStreamLines(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		buffer string
		want   []eventStreamLine
	}{
		{
			name:   "an empty buffer has no lines",
			buffer: "",
			want:   nil,
		},
		{
			name:   "LF ends a line",
			buffer: "a\nb\n",
			want:   []eventStreamLine{{content: []byte("a"), ending: []byte("\n")}, {content: []byte("b"), ending: []byte("\n")}},
		},
		{
			name:   "CR ends a line",
			buffer: "a\rb\r",
			want:   []eventStreamLine{{content: []byte("a"), ending: []byte("\r")}, {content: []byte("b"), ending: []byte("\r")}},
		},
		{
			name:   "CRLF ends a line once",
			buffer: "a\r\n\r\n",
			want:   []eventStreamLine{{content: []byte("a"), ending: []byte("\r\n")}, {content: []byte(""), ending: []byte("\r\n")}},
		},
		{
			name:   "every line keeps its own ending",
			buffer: "a\nb\r\nc\r\r\n",
			want: []eventStreamLine{
				{content: []byte("a"), ending: []byte("\n")},
				{content: []byte("b"), ending: []byte("\r\n")},
				{content: []byte("c"), ending: []byte("\r")},
				{content: []byte(""), ending: []byte("\r\n")},
			},
		},
		{
			name:   "a final line without an ending is kept",
			buffer: "a\nb",
			want:   []eventStreamLine{{content: []byte("a"), ending: []byte("\n")}, {content: []byte("b")}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			lines := splitEventStreamLines([]byte(tt.buffer))
			assert.Equal(t, tt.want, lines)

			var joined []byte
			for _, line := range lines {
				joined = append(joined, line.content...)
				joined = append(joined, line.ending...)
			}
			assert.Equal(t, tt.buffer, string(joined), "content and ending reproduce the buffer")
		})
	}
}

func TestSplitEventStreamSegments(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		buffer string
		want   []string
	}{
		{name: "an empty buffer has no segments", buffer: "", want: nil},
		{name: "LF ends a segment", buffer: "a\nb\n", want: []string{"a\n", "b\n"}},
		{name: "CRLF ends a segment with the CR kept", buffer: "a\r\n\r\n", want: []string{"a\r\n", "\r\n"}},
		{name: "a lone CR does not end a segment", buffer: "a\rb\n\n", want: []string{"a\rb\n", "\n"}},
		{name: "a final segment without LF holds the rest", buffer: "a\nb\r", want: []string{"a\n", "b\r"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var got []string
			var joined []byte
			for _, segment := range splitEventStreamSegments([]byte(tt.buffer)) {
				got = append(got, string(segment))
				joined = append(joined, segment...)
			}
			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.buffer, string(joined), "segments reproduce the buffer")
		})
	}
}
