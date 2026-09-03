// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package identitytoken

import (
	"context"
	"errors"
	"fmt"

	"github.com/sigstore/sigstore/pkg/oauthflow"
)

// ErrNoCredential is the sentinel returned when Acquire exhausts every rung
// of the ladder without obtaining a signing credential.
//
// The remediation the user reads is supplied by the calling command through
// Options.Remediation and wrapped around this sentinel rather than baked in
// here. The ladder is shared by `thv skill push` and `thv ai-plugin push`,
// but their flag sets are not: plugin signing is keyless-only, so plugin push
// defines no --key (#6442). A single hard-coded message naming every option
// would send half its readers to a flag their command does not have.
var ErrNoCredential = errors.New("signing required: no signing credential available")

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
	// Remediation names the signing choices the calling command actually
	// offers, appended to ErrNoCredential. Optional; omitting it yields the
	// bare sentinel, which is terser but still correct. See ErrNoCredential
	// for why this is caller-supplied rather than a package constant.
	Remediation string
}

// Acquire resolves the identity token to sign a skill or plugin push with,
// trying each rung of the ladder in order:
//
//  1. An explicit --identity-token is always resolved and forwarded, even
//     alongside --key — the ambiguity is a conflict for the server to
//     reject (skillsvc.validateSigningInputs), never something to silently
//     arbitrate client-side.
//  2. --key or --no-sign with no --identity-token means the user made an
//     explicit signing choice; Acquire returns "" without attempting
//     ambient or interactive acquisition. Only skill pushes can set Key;
//     plugin pushes reach this rung through --no-sign alone.
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
		return "", noCredentialError(opts.Remediation)
	}

	return Interactive(oauthflow.DefaultIDTokenGetter)
}

// noCredentialError decorates the ErrNoCredential sentinel with the calling
// command's remediation. An absent remediation yields the bare sentinel:
// terse, but never pointing at a flag the command does not define.
func noCredentialError(remediation string) error {
	if remediation == "" {
		return ErrNoCredential
	}
	return fmt.Errorf("%w. %s", ErrNoCredential, remediation)
}
