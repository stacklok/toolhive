// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/vmcp/session/internal/backend"
	"github.com/stacklok/toolhive/pkg/vmcp/session/internal/security"
	sessiontypes "github.com/stacklok/toolhive/pkg/vmcp/session/types"
)

// MultiSession is an alias for sessiontypes.MultiSession, re-exported here for
// backward compatibility and convenience.
type MultiSession = sessiontypes.MultiSession

// ListChangedSink is an alias for backend.ListChangedSink, re-exported here so
// callers outside the pkg/vmcp/session/internal/backend package (e.g.
// pkg/vmcp/server, which builds the session-registration sink) can reference it
// without importing the internal package directly.
type ListChangedSink = backend.ListChangedSink

// ValidateCaller checks caller against a stored identity-binding string (the
// value persisted under MetadataKeyIdentityBinding) and returns nil when the
// caller is permitted, or ErrNilCaller / ErrUnauthorizedCaller / ErrSessionOwnerUnknown
// otherwise.
//
// It exposes the session layer's hijack-prevention check (normally applied by
// the BindSession decorator on MultiSession.CallTool) for call paths that do
// not flow through that decorator. The Serve transport path uses it: there the
// advertised set and call routing are owned by the core, but identity binding
// must still be enforced by the session layer before a request reaches the core.
//
// The audited implementation lives in the internal security package, which only
// packages under pkg/vmcp/session may import; this is the exported seam for
// callers outside that subtree (e.g. pkg/vmcp/server).
func ValidateCaller(storedBinding string, caller *auth.Identity) error {
	return security.ValidateCaller(storedBinding, caller)
}

// Re-exports from the types package for convenience. See the types package for
// authoritative documentation.
const (
	// Legacy: superseded by MetadataKeyIdentityBinding (#5306); invalidated on read.
	MetadataKeyTokenHash = sessiontypes.MetadataKeyTokenHash
	// Legacy: superseded by MetadataKeyIdentityBinding (#5306).
	MetadataKeyTokenSalt = sessiontypes.MetadataKeyTokenSalt

	MetadataKeyIdentityBinding = sessiontypes.MetadataKeyIdentityBinding
)
