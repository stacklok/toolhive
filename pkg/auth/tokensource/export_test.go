// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokensource

import (
	"context"

	"github.com/stacklok/toolhive/pkg/auth/oauth"
)

// SetStartFlowForTest overrides the OAuth flow starter used by performBrowserFlow
// and returns a function that restores the original. Tests use this to assert how
// the SkipBrowser option is forwarded without standing up a real browser or
// callback listener. Callers must restore the original (e.g. via t.Cleanup) and
// must not run in parallel with other tests that exercise the interactive flow.
func SetStartFlowForTest(
	fn func(ctx context.Context, flow *oauth.Flow, skipBrowser bool) (*oauth.TokenResult, error),
) func() {
	orig := startFlow
	startFlow = fn
	return func() { startFlow = orig }
}
