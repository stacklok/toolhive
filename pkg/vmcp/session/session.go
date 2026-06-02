// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package session

import (
	sessiontypes "github.com/stacklok/toolhive/pkg/vmcp/session/types"
)

// MultiSession is an alias for sessiontypes.MultiSession, re-exported here for
// backward compatibility and convenience.
type MultiSession = sessiontypes.MultiSession

// Re-exports from the types package for convenience. See the types package for
// authoritative documentation.
const (
	// Legacy: superseded by MetadataKeyIdentityBinding (#5306); invalidated on read.
	MetadataKeyTokenHash = sessiontypes.MetadataKeyTokenHash
	// Legacy: superseded by MetadataKeyIdentityBinding (#5306).
	MetadataKeyTokenSalt = sessiontypes.MetadataKeyTokenSalt

	MetadataKeyIdentityBinding = sessiontypes.MetadataKeyIdentityBinding
)
