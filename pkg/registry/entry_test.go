// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"testing"

	v0 "github.com/modelcontextprotocol/registry/pkg/api/v0"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	types "github.com/stacklok/toolhive-core/registry/types"
)

func TestEntry_Validate(t *testing.T) {
	t.Parallel()

	server := &v0.ServerJSON{Name: "io.github.user/weather"}
	skill := &types.Skill{Namespace: "io.github.user", Name: "summarizer"}

	tests := []struct {
		name    string
		entry   *Entry
		wantErr string
	}{
		{
			name:  "valid server entry",
			entry: &Entry{Kind: KindServer, Name: "weather", Server: server},
		},
		{
			name:  "valid skill entry",
			entry: &Entry{Kind: KindSkill, Name: "summarizer", Skill: skill},
		},
		{
			name:    "nil entry",
			entry:   nil,
			wantErr: "nil entry",
		},
		{
			name:    "empty name",
			entry:   &Entry{Kind: KindServer, Server: server},
			wantErr: "empty name",
		},
		{
			name:    "server kind missing payload",
			entry:   &Entry{Kind: KindServer, Name: "weather"},
			wantErr: "Server is nil",
		},
		{
			name: "server kind with extra skill payload",
			entry: &Entry{
				Kind:   KindServer,
				Name:   "weather",
				Server: server,
				Skill:  skill,
			},
			wantErr: "Skill is also set",
		},
		{
			name:    "skill kind missing payload",
			entry:   &Entry{Kind: KindSkill, Name: "summarizer"},
			wantErr: "Skill is nil",
		},
		{
			name: "skill kind with extra server payload",
			entry: &Entry{
				Kind:   KindSkill,
				Name:   "summarizer",
				Skill:  skill,
				Server: server,
			},
			wantErr: "Server is also set",
		},
		{
			name:    "unknown kind",
			entry:   &Entry{Kind: Kind("widget"), Name: "thing"},
			wantErr: `unknown kind "widget"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.entry.Validate()
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}
