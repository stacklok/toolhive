// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/stacklok/toolhive/pkg/groups"
)

func TestFilterClientsAlreadyRegistered(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		clients        []ClientAppStatus
		selectedGroups []*groups.Group
		wantClients    []ClientApp
	}{
		{
			name: "no groups selected returns all clients",
			clients: []ClientAppStatus{
				{ClientType: VSCode, Installed: true},
				{ClientType: Cursor, Installed: true},
			},
			selectedGroups: nil,
			wantClients:    []ClientApp{VSCode, Cursor},
		},
		{
			name: "client registered in all selected groups is hidden",
			clients: []ClientAppStatus{
				{ClientType: VSCode, Installed: true},
				{ClientType: Cursor, Installed: true},
			},
			selectedGroups: []*groups.Group{
				{Name: "group1", RegisteredClients: []string{"vscode", "cursor"}},
				{Name: "group2", RegisteredClients: []string{"vscode"}},
			},
			wantClients: []ClientApp{Cursor},
		},
		{
			name: "client registered in only some groups is kept",
			clients: []ClientAppStatus{
				{ClientType: VSCode, Installed: true},
				{ClientType: Cursor, Installed: true},
			},
			selectedGroups: []*groups.Group{
				{Name: "group1", RegisteredClients: []string{"vscode"}},
				{Name: "group2", RegisteredClients: []string{"cursor"}},
			},
			wantClients: []ClientApp{VSCode, Cursor},
		},
		{
			name: "all clients already registered returns empty",
			clients: []ClientAppStatus{
				{ClientType: VSCode, Installed: true},
				{ClientType: Cursor, Installed: true},
			},
			selectedGroups: []*groups.Group{
				{Name: "group1", RegisteredClients: []string{"vscode", "cursor"}},
			},
			wantClients: nil,
		},
		{
			name: "single group with no registered clients returns all",
			clients: []ClientAppStatus{
				{ClientType: VSCode, Installed: true},
				{ClientType: Cursor, Installed: true},
			},
			selectedGroups: []*groups.Group{
				{Name: "group1", RegisteredClients: []string{}},
			},
			wantClients: []ClientApp{VSCode, Cursor},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := FilterClientsAlreadyRegistered(tt.clients, tt.selectedGroups)

			var gotClients []ClientApp
			for _, c := range result {
				gotClients = append(gotClients, c.ClientType)
			}

			assert.Equal(t, tt.wantClients, gotClients)
		})
	}
}
