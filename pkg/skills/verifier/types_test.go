// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package verifier

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/skills/lockfile"
)

func TestMatchIdentity(t *testing.T) {
	t.Parallel()

	result := &Result{
		Signed:         true,
		SignerIdentity: "alice@example.com",
		CertIssuer:     "https://oauth2.sigstore.dev/auth/openid",
	}

	t.Run("nil expected allows any identity", func(t *testing.T) {
		t.Parallel()
		require.NoError(t, MatchIdentity(result, nil))
	})

	t.Run("matching identity passes", func(t *testing.T) {
		t.Parallel()
		expected := &lockfile.Provenance{
			SignerIdentity: "alice@example.com",
			CertIssuer:     "https://oauth2.sigstore.dev/auth/openid",
		}
		require.NoError(t, MatchIdentity(result, expected))
	})

	t.Run("signer mismatch fails", func(t *testing.T) {
		t.Parallel()
		expected := &lockfile.Provenance{
			SignerIdentity: "bob@example.com",
			CertIssuer:     "https://oauth2.sigstore.dev/auth/openid",
		}
		err := MatchIdentity(result, expected)
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrSignerMismatch))
	})
}

func TestResultToLockProvenance(t *testing.T) {
	t.Parallel()

	assert.Nil(t, (&Result{Signed: false}).ToLockProvenance())
	prov := (&Result{
		Signed:         true,
		SignerIdentity: "alice@example.com",
		CertIssuer:     "issuer",
		RepositoryURI:  "https://github.com/org/repo",
	}).ToLockProvenance()
	require.NotNil(t, prov)
	assert.Equal(t, "alice@example.com", prov.SignerIdentity)
	assert.Equal(t, "issuer", prov.CertIssuer)
}
