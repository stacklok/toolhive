// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package session

import (
	sessiontypes "github.com/stacklok/toolhive/pkg/vmcp/session/types"
)

// MultiSession is an alias for sessiontypes.MultiSession, re-exported here for
// backward compatibility and convenience.
type MultiSession = sessiontypes.MultiSession

const (
	// MetadataKeyTokenHash is the session metadata key that holds the HMAC-SHA256
	// hash of the bearer token used to create the session. For authenticated sessions
	// this is hex(HMAC-SHA256(bearerToken)). For anonymous sessions this is the empty
	// string sentinel. The raw token is never stored — only the hash.
	//
	// Re-exported from types package for convenience.
	MetadataKeyTokenHash = sessiontypes.MetadataKeyTokenHash

	// MetadataKeyTokenSalt is the session metadata key that holds the hex-encoded
	// random salt used for HMAC-SHA256 token hashing. Omitted for anonymous sessions.
	//
	// Re-exported from types package for convenience.
	MetadataKeyTokenSalt = sessiontypes.MetadataKeyTokenSalt
)
