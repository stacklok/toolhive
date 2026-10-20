// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package session

import "context"

// MetadataKeyIdentityBinding is the canonical persisted ownership key.
const MetadataKeyIdentityBinding = "vmcp.identity.binding"

// LookupOwner loads authoritative metadata, not a live stream or backend session.
// Redis shares ownership but does not provide cross-replica stream delivery.
func (m *Manager) LookupOwner(ctx context.Context, id string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, defaultOperationTimeout)
	defer cancel()
	metadata, err := m.storage.LoadMetadata(ctx, id)
	if err != nil {
		return "", err
	}
	return metadata[MetadataKeyIdentityBinding], nil
}
