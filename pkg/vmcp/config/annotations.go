// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package config

import "github.com/stacklok/toolhive/pkg/vmcp"

// ToAnnotations converts the wire-side override into the vmcp-domain
// ToolAnnotations. It returns nil when o is nil. Pointer fields are shared
// (callers that need a private copy should deep-copy the result).
//
// This lives in the config package (not in the compositetools derivation module)
// so the pure derivation logic does not depend on the wire format — config owns
// the mapping from its override type to the domain type it already imports.
func (o *ToolAnnotationsOverride) ToAnnotations() *vmcp.ToolAnnotations {
	if o == nil {
		return nil
	}
	ann := &vmcp.ToolAnnotations{
		ReadOnlyHint:    o.ReadOnlyHint,
		DestructiveHint: o.DestructiveHint,
		IdempotentHint:  o.IdempotentHint,
		OpenWorldHint:   o.OpenWorldHint,
	}
	if o.Title != nil {
		ann.Title = *o.Title
	}
	return ann
}
