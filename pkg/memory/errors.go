// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package memory

import "errors"

// ErrNotFound is returned when a memory entry does not exist.
var ErrNotFound = errors.New("memory entry not found")

// ErrReadOnly is returned when an agent attempts to mutate an entry whose
// source type is read-only (SourceSkill or SourceResource). Use the
// management REST API to modify resource entries.
var ErrReadOnly = errors.New("memory entry is read-only")
