// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package secrets

import "github.com/stacklok/toolhive/pkg/secrets/clients"

// NewOnePasswordManagerWithClient creates an OnePasswordManager backed by the
// provided 1Password client, for tests that inject a mock client.
func NewOnePasswordManagerWithClient(client clients.OnePasswordClient) *OnePasswordManager {
	return &OnePasswordManager{
		client: client,
	}
}
