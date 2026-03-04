// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package session provides session-level token binding security for Virtual MCP Server.
//
// # Security Model
//
// Sessions implement caller identity validation at the session boundary, enforcing
// that all operations come from the authorized caller. This prevents session hijacking
// and unauthorized access to backend capabilities.
//
// Token Binding:
//   - Sessions can be "bound" (require specific caller) or "anonymous" (allow any/nil caller)
//   - Bound sessions store SHA256(bearerToken) at creation time
//   - Every method call validates the caller's token hash matches the stored hash
//   - Validation uses constant-time comparison to prevent timing side-channel attacks
//   - Token hashes are stored in both struct fields (fast validation) and metadata (persistence)
//
// # Session Types
//
// Bound Sessions:
//   - Created with an auth.Identity (non-nil)
//   - All method calls must provide matching caller identity
//   - Returns ErrUnauthorizedCaller on token mismatch
//   - Returns ErrNilCaller when nil caller provided
//
// Anonymous Sessions:
//   - Created with nil auth.Identity
//   - Accept nil or any caller identity
//   - No token validation performed
//   - Suitable for public/unauthenticated access scenarios
//
// # Error Handling
//
// Token binding errors are defined in sessiontypes package:
//   - ErrUnauthorizedCaller: Caller's token hash doesn't match session owner
//   - ErrNilCaller: Bound session received nil caller (configuration error)
//   - ErrSessionOwnerUnknown: Session has no identity but requires one (should not happen)
//
// # Implementation Details
//
// Token Hash Storage:
//  1. Struct field (boundTokenHash): Fast runtime validation in validateCaller()
//  2. Session metadata: Persistence, auditing, and backward compatibility
//
// Constant-Time Comparison:
//
//	Uses security.ConstantTimeHashCompare to prevent timing attacks that could
//	leak information about the stored token hash through response time measurements.
//
// Thread Safety:
//
//	Token binding fields are protected by sync.RWMutex, allowing concurrent
//	validation checks while preventing race conditions during session initialization.
//
// # Usage Example
//
//	// Create a bound session
//	identity := &auth.Identity{Subject: "user@example.com", Token: "secret"}
//	sess, err := factory.MakeSession(ctx, identity, backends)
//
//	// Call a tool - caller must match session owner
//	result, err := sess.CallTool(ctx, identity, "my-tool", args, meta)
//	if errors.Is(err, sessiontypes.ErrUnauthorizedCaller) {
//	    // Token mismatch - possible hijacking attempt
//	}
//
//	// Create an anonymous session
//	anonSess, err := factory.MakeSession(ctx, nil, backends)
//
//	// Call with nil caller - allowed for anonymous sessions
//	result, err = anonSess.CallTool(ctx, nil, "public-tool", args, meta)
package session
