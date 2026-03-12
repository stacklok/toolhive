// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"errors"
	"slices"

	"github.com/stacklok/toolhive/pkg/groups"
)

// ErrAllClientsRegistered is returned when all available clients are already
// registered for the selected groups.
var ErrAllClientsRegistered = errors.New("all installed clients are already registered for the selected groups")

// FilterClientsAlreadyRegistered returns only clients that are NOT already
// registered in all of the provided groups. A client is excluded only when
// every group in selectedGroups already lists it in RegisteredClients.
func FilterClientsAlreadyRegistered(
	clients []ClientAppStatus,
	selectedGroups []*groups.Group,
) []ClientAppStatus {
	if len(selectedGroups) == 0 {
		return clients
	}

	var filtered []ClientAppStatus
	for _, cli := range clients {
		if !isClientRegisteredInAllGroups(string(cli.ClientType), selectedGroups) {
			filtered = append(filtered, cli)
		}
	}
	return filtered
}

func isClientRegisteredInAllGroups(clientName string, selectedGroups []*groups.Group) bool {
	for _, group := range selectedGroups {
		if !slices.Contains(group.RegisteredClients, clientName) {
			return false
		}
	}
	return true
}
