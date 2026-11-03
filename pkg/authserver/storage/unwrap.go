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
// stor, peeling any storage decorators that expose Unwrap, and nil when no
// layer in the chain provides it. Decorators embed the Storage interface and
// so do not carry optional capabilities of the backend they wrap, which is why
// the type assertion alone is not enough — mirrors the Unwrap use at the
// ExchangeGrantStorage discovery boundary.
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
