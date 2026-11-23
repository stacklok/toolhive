// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package storage

// Unwrap returns the innermost storage backend by recursively peeling storage
// decorators that expose Unwrap. It is used at construction boundaries that
// require the durable backend, such as static-client collision checks.
func Unwrap(stor Storage) Storage {
	for {
		unwrapper, ok := stor.(interface{ Unwrap() Storage })
		if !ok {
			return stor
		}
		stor = unwrapper.Unwrap()
	}
}

// UpstreamRefreshLockerFrom returns the UpstreamRefreshLocker capability of
// stor, and nil when no layer in the chain provides it. Decorators embed the
// Storage interface and so do not carry optional capabilities of the backend
// they wrap, which is why a type assertion on stor alone is not enough; the
// ExchangeGrantStorage and DeviceCodeStorage boundaries assert on Unwrap(stor)
// for the same reason.
//
// Unlike those, this walks the chain and asserts at every layer rather than
// only at the base. A decorator that implements the capability itself — to
// coordinate against something the base backend does not know about — is then
// honoured, and a chain whose decorator does not expose Unwrap still resolves
// whatever the outer layers provide instead of silently returning nil. The
// cost is a type assertion per layer, once at construction.
func UpstreamRefreshLockerFrom(stor UpstreamTokenStorage) UpstreamRefreshLocker {
	for stor != nil {
		if locker, ok := stor.(UpstreamRefreshLocker); ok {
			return locker
		}
		unwrapper, ok := stor.(interface{ Unwrap() Storage })
		if !ok {
			return nil
		}
		stor = unwrapper.Unwrap()
	}
	return nil
}
