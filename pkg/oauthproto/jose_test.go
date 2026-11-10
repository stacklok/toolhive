// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package oauthproto

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsJOSEType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		headerTyp string
		mediaType string
		want      bool
	}{
		{name: "bare match", headerTyp: "oauth-id-jag+jwt", mediaType: JWTTypeIDJAG, want: true},
		{name: "header with application prefix", headerTyp: "application/oauth-id-jag+jwt", mediaType: JWTTypeIDJAG, want: true},
		{name: "media type with application prefix", headerTyp: "oauth-id-jag+jwt", mediaType: "application/oauth-id-jag+jwt", want: true},
		{name: "prefix on both", headerTyp: "application/oauth-id-jag+jwt", mediaType: "application/oauth-id-jag+jwt", want: true},
		{name: "upper case", headerTyp: "OAUTH-ID-JAG+JWT", mediaType: JWTTypeIDJAG, want: true},
		{name: "mixed case with prefix", headerTyp: "Application/OAuth-ID-JAG+Jwt", mediaType: JWTTypeIDJAG, want: true},
		{name: "plain JWT", headerTyp: "JWT", mediaType: JWTTypeIDJAG, want: false},
		{name: "at+jwt", headerTyp: "at+jwt", mediaType: JWTTypeIDJAG, want: false},
		{name: "application/at+jwt", headerTyp: "application/at+jwt", mediaType: JWTTypeIDJAG, want: false},
		{name: "trailing suffix", headerTyp: "oauth-id-jag+jwtx", mediaType: JWTTypeIDJAG, want: false},
		{name: "leading prefix text", headerTyp: "xoauth-id-jag+jwt", mediaType: JWTTypeIDJAG, want: false},
		{name: "other top-level type", headerTyp: "text/oauth-id-jag+jwt", mediaType: JWTTypeIDJAG, want: false},
		{name: "empty header", headerTyp: "", mediaType: JWTTypeIDJAG, want: false},
		{name: "header is only the prefix", headerTyp: "application/", mediaType: JWTTypeIDJAG, want: false},
		{name: "both empty", headerTyp: "", mediaType: "", want: false},
		{name: "empty media type", headerTyp: "JWT", mediaType: "", want: false},
		{name: "both only the prefix", headerTyp: "application/", mediaType: "application/", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, IsJOSEType(tt.headerTyp, tt.mediaType))
		})
	}
}
