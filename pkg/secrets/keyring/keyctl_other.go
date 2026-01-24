// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

//go:build !linux

package keyring

import "fmt"

// NewKeyctlProvider creates a new keyctl provider. This provider is only available on Linux.
func NewKeyctlProvider() (Provider, error) {
	return nil, fmt.Errorf("keyctl provider is only available on Linux")
}
