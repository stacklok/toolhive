// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authorizationdetails

import (
	"errors"
	"fmt"
	"regexp"
)

var (
	// ErrInvalid reports that the authorization_details parameter is malformed:
	// bad JSON, over a size or depth limit, duplicate member names, an entry
	// that is not an object, or a missing, unknown or wrong-grant "type".
	// Callers should answer with a generic invalid_authorization_details error
	// and must not echo request data.
	ErrInvalid = errors.New("invalid authorization_details")

	// ErrUnknownType reports that every entry is well formed but at least one
	// has a "type" that is not registered for the grant type. It wraps
	// ErrInvalid, so errors.Is(err, ErrInvalid) still holds; a caller that
	// tells the two apart checks ErrUnknownType first.
	ErrUnknownType = fmt.Errorf("%w: unknown type", ErrInvalid)

	// errInvalidHookResult reports that a type's Grant hook returned output the
	// package rejects: an empty result from Grant, an entry that is not an
	// object of that type, an entry GrantSubset cannot match to one of the
	// type's entries, a limit violation, or a typed Error with an unknown Code
	// or an invalid Reason. It is a defect in the registered hook, not in the
	// request, so callers should treat it like any other hook failure.
	errInvalidHookResult = errors.New("invalid authorization details hook result")
)

// ErrorCode is the OAuth error code a hook may return to the client.
type ErrorCode string

const (
	// CodeInvalidAuthorizationDetails is the RFC 9396 invalid_authorization_details error.
	CodeInvalidAuthorizationDetails ErrorCode = "invalid_authorization_details"
	// CodeInvalidRequest is the RFC 6749 invalid_request error. It is for
	// problems outside the authorization details themselves, such as a missing
	// or unacceptable related parameter. Problems in the details' content
	// (unknown fields, wrong types, invalid values, missing required fields)
	// must use CodeInvalidAuthorizationDetails, per RFC 9396 section 5.
	CodeInvalidRequest ErrorCode = "invalid_request"
)

var reasonPattern = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)

// Error is a client-visible rejection a Grant hook may return. Reason is shown
// to the client, so it must match [a-z0-9_]{1,64} and must not carry request
// data. Any other error a hook returns is treated as an internal failure.
type Error struct {
	Code   ErrorCode
	Reason string
}

// Error implements error.
func (e Error) Error() string {
	return string(e.Code) + ": " + e.Reason
}

func (e Error) valid() bool {
	switch e.Code {
	case CodeInvalidAuthorizationDetails, CodeInvalidRequest:
	default:
		return false
	}
	return reasonPattern.MatchString(e.Reason)
}
