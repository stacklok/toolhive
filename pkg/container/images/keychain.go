// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package images

import (
	"github.com/google/go-containerregistry/pkg/authn"

	coreimages "github.com/stacklok/toolhive-core/container/images"
)

// NewCompositeKeychain creates a keychain that tries environment variables first,
// then falls back to the default keychain.
// Deprecated: use github.com/stacklok/toolhive-core/container/images.NewCompositeKeychain.
func NewCompositeKeychain() authn.Keychain {
	return coreimages.NewCompositeKeychain()
}
