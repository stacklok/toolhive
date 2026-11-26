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

// storageDecorator is implemented by ToolHive's own storage decorators. They
// leave provider-identity rows to the Storage they wrap, so a capability
// lookup may look underneath them. Its method is unexported, so types outside
// this package cannot implement it: an external wrapper stops the lookup even
// if it has Unwrap, and must implement the capability itself to offer it.
type storageDecorator interface {
	Unwrap() Storage
	storageDecorator()
}

func (*UserKeyedUpstreamStorage) storageDecorator() {}
func (*ClientAdmissionStorage) storageDecorator()   {}
func (*SPIFFEStorageDecorator) storageDecorator()   {}
func (*CIMDStorageDecorator) storageDecorator()     {}
func (cimdShapeGuardStorage) storageDecorator()     {}

// ProviderIdentitySwapperFor returns the outermost ProviderIdentitySwapper on
// stor. Unwrap says how to walk a decorator stack; storageDecorator says it is
// safe to look underneath. The walk follows only storageDecorator, never
// Unwrap alone, so a wrapper written outside this package is not bypassed to
// write directly to the store beneath it, since it may serve identity rows
// itself. Do not simplify this to Unwrap.
func ProviderIdentitySwapperFor(stor UserStorage) ProviderIdentitySwapper {
	for stor != nil {
		if swapper, ok := stor.(ProviderIdentitySwapper); ok {
			return swapper
		}
		d, ok := stor.(storageDecorator)
		if !ok {
			return nil
		}
		stor = d.Unwrap()
	}
	return nil
}
