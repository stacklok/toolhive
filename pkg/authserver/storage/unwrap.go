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

// ProviderIdentitySwapperFor returns the outermost ProviderIdentitySwapper on
// stor. It peels only ToolHive's own storage decorators (which do not intercept
// provider-identity rows) so an external decorator — even one that exposes
// Unwrap() Storage — is never bypassed to write directly to a backing store
// underneath it.
func ProviderIdentitySwapperFor(stor UserStorage) ProviderIdentitySwapper {
	for stor != nil {
		if swapper, ok := stor.(ProviderIdentitySwapper); ok {
			return swapper
		}
		switch d := stor.(type) {
		case *UserKeyedUpstreamStorage:
			stor = d.Storage
		case *ClientAdmissionStorage:
			stor = d.Storage
		case *SPIFFEStorageDecorator:
			stor = d.Storage
		case *CIMDStorageDecorator:
			stor = d.Storage
		case cimdShapeGuardStorage:
			stor = d.Storage
		default:
			return nil
		}
	}
	return nil
}
