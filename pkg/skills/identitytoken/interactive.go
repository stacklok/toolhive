// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package identitytoken

import (
	"fmt"

	"github.com/sigstore/sigstore/pkg/oauthflow"
)

// Public-good Sigstore OAuth instance (oauth2.sigstore.dev), the same
// values cosign uses for its interactive `--identity-token` login. There is
// no toolhive-specific OAuth application — every Sigstore-signing tool
// authenticates against this same public client.
const (
	sigstoreOAuthIssuer   = "https://oauth2.sigstore.dev/auth"
	sigstoreOAuthClientID = "sigstore"
)

// Interactive obtains an OIDC identity token via an interactive browser
// sign-in against the public-good Sigstore OAuth instance. tg is injectable
// for tests (see oauthflow.StaticTokenGetter); production callers pass
// oauthflow.DefaultIDTokenGetter.
//
// This blocks until the user completes or abandons the browser flow.
// oauthflow.OIDConnect accepts no context and hardcodes context.Background()
// internally, so there is no cancellation path to plumb here: the redirect
// wait self-limits to 120s, but provider discovery, the code exchange, and
// the out-of-band stdin fallback are all unbounded. That's acceptable
// because this only runs after an explicit y/N confirmation on a TTY — a
// human is already present, and Ctrl-C is the exit. Wrapping this in a
// goroutine would leak one blocked on stdin or a listening socket with no
// way to cancel it, which is worse than an unbounded wait a human can
// interrupt themselves.
func Interactive(tg oauthflow.TokenGetter) (string, error) {
	tok, err := oauthflow.OIDConnect(sigstoreOAuthIssuer, sigstoreOAuthClientID, "", "", tg)
	if err != nil {
		return "", fmt.Errorf("acquiring identity token via browser sign-in: %w", err)
	}
	return tok.RawString, nil
}
