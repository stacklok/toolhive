// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"errors"
	"fmt"
)

// ErrDelegatedCredentialUnavailable indicates the delegated credential could not
// be read and the client cannot remedy the failure by reconnecting the provider.
var ErrDelegatedCredentialUnavailable = errors.New("upstream credential unavailable")

// DelegatedAuthRequiredError identifies the provider the user must authorize.
type DelegatedAuthRequiredError struct {
	Provider string
	cause    error
}

// NewDelegatedAuthRequiredError reports that provider needs authorization.
// Pass only a classification sentinel as cause, not the underlying read error.
func NewDelegatedAuthRequiredError(provider string, cause error) *DelegatedAuthRequiredError {
	return &DelegatedAuthRequiredError{Provider: provider, cause: cause}
}

func (e *DelegatedAuthRequiredError) Error() string {
	if e == nil || e.Provider == "" {
		return "upstream authorization required"
	}
	return fmt.Sprintf("provider %q requires upstream authorization", e.Provider)
}

// Unwrap returns the cause for error classification.
func (e *DelegatedAuthRequiredError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

// DelegatedCredentialError returns the bare delegated credential error in err's
// chain, preferring authorization required over credential unavailable. It returns
// nil if no delegated credential error is present.
func DelegatedCredentialError(err error) error {
	var authReq *DelegatedAuthRequiredError
	if errors.As(err, &authReq) && authReq != nil {
		return authReq
	}
	if errors.Is(err, ErrDelegatedCredentialUnavailable) {
		return ErrDelegatedCredentialUnavailable
	}
	return nil
}
