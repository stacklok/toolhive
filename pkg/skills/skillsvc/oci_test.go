// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package skillsvc

import (
	"testing"

	nameref "github.com/google/go-containerregistry/pkg/name"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestQualifiedOCIRef(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "explicit tag unchanged",
			input: "ghcr.io/org/my-skill:v1",
			want:  "ghcr.io/org/my-skill:v1",
		},
		{
			name:  "no tag defaults to latest",
			input: "ghcr.io/stacklok/toolhive/skills/toolhive-cli-user",
			want:  "ghcr.io/stacklok/toolhive/skills/toolhive-cli-user:latest",
		},
		{
			name:  "digest unchanged",
			input: "ghcr.io/org/my-skill@sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
			want:  "ghcr.io/org/my-skill@sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ref, err := nameref.ParseReference(tt.input)
			require.NoError(t, err)
			assert.Equal(t, tt.want, qualifiedOCIRef(ref))
		})
	}
}
