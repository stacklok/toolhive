// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"fmt"

	"github.com/stacklok/toolhive/pkg/memory"
)

// checkMutable returns an error if the entry's source type is read-only to
// agents (SourceSkill or SourceResource). These entries may only be modified
// via the management REST API, not via MCP tool calls.
func checkMutable(ctx context.Context, store memory.Store, id string) error {
	entry, err := store.Get(ctx, id)
	if err != nil {
		return err
	}
	if entry.Source == memory.SourceSkill || entry.Source == memory.SourceResource {
		return fmt.Errorf("entry %q (source=%s): %w", id, entry.Source, memory.ErrReadOnly)
	}
	return nil
}
