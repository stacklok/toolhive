// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package conversion_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	sdkmcp "github.com/stacklok/toolhive-core/mcpcompat/mcp"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/conversion"
)

type codedErr struct{}

func (codedErr) Error() string        { return "rate limited" }
func (codedErr) Code() int64          { return -32029 }
func (codedErr) Data() map[string]any { return map[string]any{"retryAfterSeconds": 3} }

func TestErrorToToolResult(t *testing.T) {
	t.Parallel()

	github, err := vmcp.NewURLElicitation("github", "https://connect.example.com/github", "Reconnect GitHub")
	require.NoError(t, err)
	jira, err := vmcp.NewURLElicitation("jira", "https://connect.example.com/jira", "Reconnect Jira")
	require.NoError(t, err)
	one, err := vmcp.NewURLElicitationRequiredError(github)
	require.NoError(t, err)
	two, err := vmcp.NewURLElicitationRequiredError(github, jira)
	require.NoError(t, err)
	withFallback, err := one.WithLegacyFallback("Reconnect GitHub from the Connectors page")
	require.NoError(t, err)

	tests := []struct {
		name           string
		err            error
		wantText       string
		wantStructured bool
	}{
		{
			name:     "authorization denial",
			err:      fmt.Errorf("call: %w", vmcp.ErrAuthorizationFailed),
			wantText: vmcp.DenyMessageToolCall,
		},
		{
			name:     "URL elicitation answer renders its message",
			err:      fmt.Errorf("backend github: %w", one),
			wantText: "Reconnect GitHub",
		},
		{
			name:     "several elicitations render every message",
			err:      two,
			wantText: "Reconnect GitHub; Reconnect Jira",
		},
		{
			name:     "embedder fallback replaces the message",
			err:      fmt.Errorf("backend github: %w", withFallback),
			wantText: "Reconnect GitHub from the Connectors page",
		},
		{
			name:     "denial outranks the URL elicitation answer",
			err:      fmt.Errorf("%w: %w", vmcp.ErrAuthorizationFailed, one),
			wantText: vmcp.DenyMessageToolCall,
		},
		{
			name:           "coded error keeps its code and data",
			err:            codedErr{},
			wantText:       "rate limited",
			wantStructured: true,
		},
		{
			name:     "generic error renders its text",
			err:      errors.New("backend exploded"),
			wantText: "backend exploded",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := conversion.ErrorToToolResult(tt.err)
			require.True(t, got.IsError)
			require.Len(t, got.Content, 1)
			text, ok := got.Content[0].(sdkmcp.TextContent)
			require.True(t, ok, "%T", got.Content[0])
			assert.Equal(t, tt.wantText, text.Text)
			assert.NotContains(t, text.Text, "connect.example.com")
			if tt.wantStructured {
				assert.NotNil(t, got.StructuredContent)
			} else {
				assert.Nil(t, got.StructuredContent)
			}
		})
	}
}
