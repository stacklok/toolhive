// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package skillsvc

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/skills"
	"github.com/stacklok/toolhive/pkg/skills/verifier"
)

func TestClassifySignatureError(t *testing.T) {
	t.Parallel()

	assert.Equal(t, skills.FailureReasonSignerMismatch, classifySignatureError(verifier.ErrSignerMismatch))
	assert.Equal(t, skills.FailureReasonUnsignedRejected, classifySignatureError(verifier.ErrUnsigned))
	assert.Equal(t, skills.FailureReasonSignatureInvalid, classifySignatureError(verifier.ErrSignatureInvalid))
}

func TestProvenanceInfoToLockRoundTrip(t *testing.T) {
	t.Parallel()

	info := &skills.ProvenanceInfo{
		SignerIdentity: "alice@example.com",
		CertIssuer:     "issuer",
		RepositoryURI:  "https://github.com/org/repo",
		SigstoreURL:    "https://rekor.sigstore.dev",
	}
	lockProv := provenanceInfoToLock(info)
	require.NotNil(t, lockProv)
	roundTrip := provenanceInfoFromLock(lockProv)
	assert.Equal(t, info, roundTrip)
}
