// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package skillsvc

import (
	"context"

	"github.com/stacklok/toolhive/pkg/skills/lockfile"
	"github.com/stacklok/toolhive/pkg/skills/verifier"
)

// permissiveTestVerifier treats artifacts as unsigned so project-scope tests
// can install without Sigstore fixtures unless they inject a stricter verifier.
type permissiveTestVerifier struct{}

func (permissiveTestVerifier) VerifyOCI(context.Context, string, string) (*verifier.Result, error) {
	return nil, verifier.ErrUnsigned
}

func (permissiveTestVerifier) VerifyGit(context.Context, string, string) (*verifier.Result, error) {
	return nil, verifier.ErrUnsigned
}

func (permissiveTestVerifier) VerifyBundleOffline([]byte, string, *lockfile.Provenance) error {
	return nil
}

func (permissiveTestVerifier) ResultFromBundle([]byte, string) (*verifier.Result, error) {
	return nil, verifier.ErrUnsigned
}

// withTestVerifier returns a service option that bypasses live Sigstore verification.
func withTestVerifier() Option {
	return WithSigVerifier(permissiveTestVerifier{})
}
