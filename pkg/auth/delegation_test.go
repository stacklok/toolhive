// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// nestedAct builds an act claim value with the given actor subjects nested
// outermost-first: nestedAct("a", "b") => {"sub":"a","act":{"sub":"b"}}.
func nestedAct(subs ...string) map[string]any {
	var current map[string]any
	for i := len(subs) - 1; i >= 0; i-- {
		m := map[string]any{"sub": subs[i]}
		if current != nil {
			m["act"] = current
		}
		current = m
	}
	return current
}

func actorSubjects(chain DelegationChain) []string {
	var subs []string
	for _, a := range chain.Actors {
		subs = append(subs, a.Subject)
	}
	return subs
}

func TestParseDelegationChain(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		act           any
		maxDepth      int
		wantSubjects  []string
		wantTruncated bool
	}{
		{
			name:         "no act claim",
			act:          nil,
			maxDepth:     DefaultMaxDelegationDepth,
			wantSubjects: nil,
		},
		{
			name:         "single actor",
			act:          map[string]any{"sub": "agent-1"},
			maxDepth:     DefaultMaxDelegationDepth,
			wantSubjects: []string{"agent-1"},
		},
		{
			name:         "nested three deep, outermost first",
			act:          nestedAct("agent-1", "agent-2", "agent-3"),
			maxDepth:     DefaultMaxDelegationDepth,
			wantSubjects: []string{"agent-1", "agent-2", "agent-3"},
		},
		{
			name:         "non-map act yields empty chain",
			act:          "agent-1",
			maxDepth:     DefaultMaxDelegationDepth,
			wantSubjects: nil,
		},
		{
			name:         "non-map nested act stops the chain",
			act:          map[string]any{"sub": "agent-1", "act": "not-a-map"},
			maxDepth:     DefaultMaxDelegationDepth,
			wantSubjects: []string{"agent-1"},
		},
		{
			name:         "exactly maxDepth actors is not truncated",
			act:          nestedAct("a1", "a2", "a3"),
			maxDepth:     3,
			wantSubjects: []string{"a1", "a2", "a3"},
		},
		{
			name:          "maxDepth plus one is truncated",
			act:           nestedAct("a1", "a2", "a3", "a4"),
			maxDepth:      3,
			wantSubjects:  []string{"a1", "a2", "a3"},
			wantTruncated: true,
		},
		{
			name:         "zero maxDepth uses default",
			act:          nestedAct("agent-1", "agent-2"),
			maxDepth:     0,
			wantSubjects: []string{"agent-1", "agent-2"},
		},
		{
			name:         "negative maxDepth uses default",
			act:          nestedAct("agent-1"),
			maxDepth:     -5,
			wantSubjects: []string{"agent-1"},
		},
		{
			name: "default depth accommodates the issuance cap",
			act: nestedAct(
				"a1", "a2", "a3", "a4", "a5", "a6", "a7", "a8", "a9", "a10"),
			maxDepth: 0,
			wantSubjects: []string{
				"a1", "a2", "a3", "a4", "a5", "a6", "a7", "a8", "a9", "a10"},
		},
		{
			name:         "act without sub keeps extra claims",
			act:          map[string]any{"iss": "https://issuer.example", "client_id": "c-1"},
			maxDepth:     DefaultMaxDelegationDepth,
			wantSubjects: []string{""},
		},
		{
			name:         "explicit nil nested act ends the chain",
			act:          map[string]any{"sub": "agent-1", "act": nil},
			maxDepth:     DefaultMaxDelegationDepth,
			wantSubjects: []string{"agent-1"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			chain := ParseDelegationChain(tt.act, tt.maxDepth)

			assert.Equal(t, tt.wantSubjects, actorSubjects(chain))
			assert.Equal(t, tt.wantTruncated, chain.Truncated)
		})
	}
}

func TestParseDelegationChain_ActorClaims(t *testing.T) {
	t.Parallel()

	act := map[string]any{
		"sub":       "agent-1",
		"iss":       "https://issuer.example",
		"client_id": "c-1",
		"act": map[string]any{
			"sub": "agent-2",
		},
	}

	chain := ParseDelegationChain(act, DefaultMaxDelegationDepth)
	require.Len(t, chain.Actors, 2)

	assert.Equal(t, "agent-1", chain.Actors[0].Subject)
	assert.Equal(t, map[string]any{
		"iss":       "https://issuer.example",
		"client_id": "c-1",
	}, chain.Actors[0].Claims, "extra act members must be preserved, sub/act excluded")

	assert.Equal(t, "agent-2", chain.Actors[1].Subject)
	assert.Empty(t, chain.Actors[1].Claims, "an act with only sub yields no extra claims")
}

func TestParseDelegationChain_DefaultDepthTruncatesBeyondCap(t *testing.T) {
	t.Parallel()

	subs := make([]string, 0, DefaultMaxDelegationDepth+1)
	for i := 1; i <= DefaultMaxDelegationDepth+1; i++ {
		subs = append(subs, fmt.Sprintf("a%d", i))
	}

	chain := ParseDelegationChain(nestedAct(subs...), DefaultMaxDelegationDepth)

	require.Len(t, chain.Actors, DefaultMaxDelegationDepth)
	assert.True(t, chain.Truncated)
	assert.Equal(t, 1, chain.DroppedCount)
	assert.Equal(t, subs[:DefaultMaxDelegationDepth], actorSubjects(chain))
}

// TestParseDelegationChain_DroppedCount pins that the dropped count reports
// every actor cut by the depth cap, not just whether truncation happened.
func TestParseDelegationChain_DroppedCount(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		act         any
		maxDepth    int
		wantDropped int
	}{
		{
			name:        "no truncation reports zero",
			act:         nestedAct("a1", "a2"),
			maxDepth:    2,
			wantDropped: 0,
		},
		{
			name:        "one actor beyond the cap",
			act:         nestedAct("a1", "a2", "a3"),
			maxDepth:    2,
			wantDropped: 1,
		},
		{
			name:        "several actors beyond the cap",
			act:         nestedAct("a1", "a2", "a3", "a4", "a5"),
			maxDepth:    2,
			wantDropped: 3,
		},
		{
			name:        "non-map remainder counts as one dropped entry",
			act:         map[string]any{"sub": "a1", "act": map[string]any{"sub": "a2", "act": "not-a-map"}},
			maxDepth:    1,
			wantDropped: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			chain := ParseDelegationChain(tt.act, tt.maxDepth)

			assert.Equal(t, tt.wantDropped, chain.DroppedCount)
			assert.Equal(t, tt.wantDropped > 0, chain.Truncated)
		})
	}
}
