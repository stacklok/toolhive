// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package config

// ProviderFactory is a function that optionally creates a Provider.
// Returning nil signals that the caller should fall back to the default provider.
type ProviderFactory func() Provider

// registeredFactory is the package-level factory, nil by default.
var registeredFactory ProviderFactory

// RegisterProviderFactory sets a custom factory to be used by NewProvider.
// It must be called before the first call to NewProvider (typically in main or init).
// Calling it a second time replaces the previously registered factory.
func RegisterProviderFactory(f ProviderFactory) {
	registeredFactory = f
}
