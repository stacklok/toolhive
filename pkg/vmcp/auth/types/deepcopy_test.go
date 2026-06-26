// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOBOConfig_DeepCopy_Independence(t *testing.T) {
	t.Parallel()

	skew := int32(30)
	orig := &OBOConfig{
		TokenURL:                 "https://login.microsoftonline.com/tenant/oauth2/v2.0/token",
		ClientID:                 "client-id",
		Audience:                 "api://backend",
		Scopes:                   []string{"scope1", "scope2"},
		ClientSecret:             "secret",
		ClientSecretEnv:          "SECRET_ENV",
		SubjectTokenProviderName: "entra",
		CacheSkewSeconds:         &skew,
	}

	cloned := orig.DeepCopy()
	require.NotNil(t, cloned)

	// Mutate the cloned struct's slice and pointer fields — the original must be unchanged.
	cloned.Scopes[0] = "mutated"
	*cloned.CacheSkewSeconds = 99

	assert.Equal(t, "scope1", orig.Scopes[0], "DeepCopy must produce an independent Scopes slice")
	assert.Equal(t, int32(30), *orig.CacheSkewSeconds, "DeepCopy must produce an independent CacheSkewSeconds pointer")
}
