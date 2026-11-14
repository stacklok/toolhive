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
