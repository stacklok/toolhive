// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package toxicflow

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKeywordInferenceInfer(t *testing.T) {
	t.Parallel()

	ki := NewKeywordInference()
	tests := []struct {
		name       string
		profile    ServerProfile
		wantSource bool
	}{
		{"web tag", ServerProfile{Tags: []string{"web", "utility"}}, true},
		{"fetch tool name", ServerProfile{Tools: []string{"fetch_page"}}, true},
		{"description mentions web", ServerProfile{Description: "Fetches web pages and returns their content"}, true},
		{"innocuous description", ServerProfile{Description: "Performs arithmetic calculations locally"}, false},
		{"substring is not a match", ServerProfile{Description: "manages a cobweb of dependencies"}, false},
		{"empty profile", ServerProfile{Name: "x"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			hints, err := ki.Infer(t.Context(), tt.profile)
			require.NoError(t, err)
			assert.Equal(t, tt.wantSource, hasHint(hints, RoleSource))
			for _, h := range hints {
				assert.Equal(t, ConfPossible, h.Confidence, "keyword hints must be possible")
			}
		})
	}
}

type stubCompleter struct {
	out string
	err error
}

func (s stubCompleter) Complete(_ context.Context, _, _ string) (string, error) {
	return s.out, s.err
}

func TestLLMInferenceInfer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		out        string
		cErr       error
		wantSource bool
		wantData   bool
		wantErr    bool
	}{
		{
			name:       "both legs true",
			out:        `{"untrusted_content":true,"private_data":true,"reason":"github account"}`,
			wantSource: true, wantData: true,
		},
		{
			name:       "only untrusted content",
			out:        `{"untrusted_content":true,"private_data":false,"reason":"web search"}`,
			wantSource: true,
		},
		{
			name: "neither leg",
			out:  `{"untrusted_content":false,"private_data":false,"reason":"local calculator"}`,
		},
		{
			name:       "json wrapped in prose and fences",
			out:        "Sure:\n```json\n{\"untrusted_content\":true,\"private_data\":false}\n```",
			wantSource: true,
		},
		{
			name:    "non-json response is an error",
			out:     "I cannot help with that",
			wantErr: true,
		},
		{
			name:    "completer error propagates",
			cErr:    errors.New("network down"),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			li := NewLLMInference(stubCompleter{out: tt.out, err: tt.cErr})
			hints, err := li.Infer(t.Context(), ServerProfile{Name: "x"})
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantSource, hasHint(hints, RoleSource))
			assert.Equal(t, tt.wantData, hasHint(hints, RoleData))
			for _, h := range hints {
				assert.Equal(t, ConfPossible, h.Confidence, "llm hints must be capped at possible")
			}
		})
	}
}

func hasHint(hints []Hint, role Role) bool {
	for _, h := range hints {
		if h.Role == role {
			return true
		}
	}
	return false
}
