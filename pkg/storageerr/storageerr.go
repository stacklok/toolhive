// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package storageerr defines error sentinels shared between persistence
// backends and the packages that consume them without importing the backend.
//
// It depends only on the standard library so that both sides of a storage
// boundary can match the same error value with errors.Is.
package storageerr

import "errors"

// ErrNotFound is returned when an item is not found in storage. Backends wrap
// or return this exact value; callers must match it with errors.Is. The
// message text is not part of the contract.
var ErrNotFound = errors.New("storage: item not found")
