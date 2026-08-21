// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package identitytoken

import (
	"context"
	"errors"

	"github.com/sigstore/sigstore/pkg/oauthflow"
)

// ErrNoCredential is returned when Acquire exhausts every rung of the
// ladder without obtaining a signing credential. The message names every
// remaining option so the failure is actionable from the CLI alone.
var ErrNoCredential = errors.New("signing required: no signing credential available. " +
	"Provide --key, --identity-token, run in CI with id-token: write permission, or pass --no-sign to push unsigned")

// Options configures Acquire.
type Options struct {
	// FlagValue is the raw --identity-token flag value, if given.
	FlagValue string
	// Key is the --key flag value, if given.
	Key string
	// NoSign is the --no-sign flag value.
	NoSign bool
	// Confirm is called only when no token was found ambiently, to ask the
	// user (via the CLI's own TTY-gated prompt) whether to open a browser
	// for an interactive sign-in. Returning (false, nil) declines or skips
	// without error. Required.
	Confirm func() (bool, error)
}

// Acquire resolves the identity token to sign a skill push with, trying
// each rung of the ladder in order:
//
//  1. An explicit --identity-token is always resolved and forwarded, even
//     alongside --key — the ambiguity is a conflict for the server to
//     reject (skillsvc.validateSigningInputs), never something to silently
//     arbitrate client-side.
//  2. --key or --no-sign with no --identity-token means the user made an
//     explicit signing choice; Acquire returns "" without attempting
//     ambient or interactive acquisition.
//  3. A GitHub Actions ambient OIDC token, when present.
//  4. An interactive browser sign-in, gated by opts.Confirm.
//  5. ErrNoCredential.
func Acquire(ctx context.Context, opts Options) (string, error) {
	if opts.Confirm == nil {
		return "", errors.New("identitytoken: Options.Confirm is required")
	}
	if opts.FlagValue != "" {
		return Resolve(opts.FlagValue)
	}
	if opts.Key != "" || opts.NoSign {
		return "", nil
	}

	if token, ok, err := Ambient(ctx); err != nil {
		return "", err
	} else if ok {
		return token, nil
	}

	confirmed, err := opts.Confirm()
	if err != nil {
		return "", err
	}
	if !confirmed {
		return "", ErrNoCredential
	}

	return Interactive(oauthflow.DefaultIDTokenGetter)
}
