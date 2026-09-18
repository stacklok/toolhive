// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package deviceflow

import (
	"net/http"

	"github.com/ory/fosite"
)

// ErrAuthorizationPending indicates the device flow is still awaiting the
// end user's action at the verification URI (RFC 8628 Section 3.5).
var ErrAuthorizationPending = &fosite.RFC6749Error{
	ErrorField:       "authorization_pending",
	DescriptionField: "The authorization request is still pending as the end user hasn't yet completed the user-interaction steps.",
	CodeField:        http.StatusBadRequest,
}

// ErrSlowDown indicates the client polled faster than the granted interval
// (RFC 8628 Section 3.5).
var ErrSlowDown = &fosite.RFC6749Error{
	ErrorField:       "slow_down",
	DescriptionField: "The client polled the token endpoint faster than the interval permitted.",
	CodeField:        http.StatusBadRequest,
}

// ErrExpiredToken indicates the device_code has expired and the client must
// restart the device authorization flow (RFC 8628 Section 3.5).
//
// This is deliberately its own sentinel rather than a reuse of fosite's
// fosite.ErrTokenExpired: that error's wire "error" field is "invalid_token"
// (RFC 6750 Section 3.1's bearer-token-error vocabulary), not RFC 8628
// Section 3.5's "expired_token". Reusing it would emit the wrong error code
// to device-flow clients.
var ErrExpiredToken = &fosite.RFC6749Error{
	ErrorField:       "expired_token",
	DescriptionField: "The device_code has expired. The client must restart the device authorization flow.",
	CodeField:        http.StatusBadRequest,
}
