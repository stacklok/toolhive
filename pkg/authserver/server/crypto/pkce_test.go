// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package crypto

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGeneratePKCEVerifier(t *testing.T) {
	t.Parallel()

	verifier := GeneratePKCEVerifier()

	// RFC 7636: code_verifier must be 43-128 characters
	assert.GreaterOrEqual(t, len(verifier), 43)
	assert.LessOrEqual(t, len(verifier), 128)
}

func TestComputePKCEChallenge_RFC7636Example(t *testing.T) {
	t.Parallel()

	// RFC 7636 Appendix B example
	verifier := "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	expected := "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"

	assert.Equal(t, expected, ComputePKCEChallenge(verifier))
}
