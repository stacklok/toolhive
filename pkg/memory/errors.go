// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package memory

import "errors"

// ErrNotFound is returned when a memory entry does not exist.
var ErrNotFound = errors.New("memory entry not found")
