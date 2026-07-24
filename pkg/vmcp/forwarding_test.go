// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package vmcp

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestListChangedKindForMethod covers the shared method->kind classification
// used by both the backend client's OnNotification handler
// (pkg/vmcp/client/forwarding.go) and the persistent session connector
// (pkg/vmcp/session/internal/backend/mcp_session.go).
func TestListChangedKindForMethod(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		method   string
		wantKind ListChangedKind
		wantOK   bool
	}{
		{"tools list_changed", MethodToolListChanged, ListChangedTools, true},
		{"resources list_changed", MethodResourceListChanged, ListChangedResources, true},
		{"prompts list_changed", MethodPromptListChanged, ListChangedPrompts, true},
		{"progress notification is not a list_changed kind", MethodProgressNotification, 0, false},
		{"log notification is not a list_changed kind", MethodLogNotification, 0, false},
		{"unknown method", "notifications/resources/updated", 0, false},
		{"empty method", "", 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			kind, ok := ListChangedKindForMethod(tt.method)
			assert.Equal(t, tt.wantOK, ok)
			if tt.wantOK {
				assert.Equal(t, tt.wantKind, kind)
			}
		})
	}
}
