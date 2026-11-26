// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package actor

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEffective(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		source string
		claims map[string]any
		client string
		want   *Actor
		// wantClaims, when set, is the exact Claims the actor must carry.
		wantClaims map[string]any
		wantErr    error
		// wantFail is for errors that carry no sentinel.
		wantFail bool
		noIssuer bool
	}{
		{
			name: "outermost act wins over a nested one",
			claims: map[string]any{"act": map[string]any{
				"iss": "https://platform", "sub": "agent-1",
				"act": map[string]any{"iss": "https://other", "sub": "agent-0"},
			}},
			want:       &Actor{Source: SourceAct, Issuer: "https://platform", Subject: "agent-1"},
			wantClaims: map[string]any{"iss": "https://platform", "sub": "agent-1"},
		},
		{
			name:   "act without iss takes the token issuer",
			claims: map[string]any{"act": map[string]any{"sub": "agent-1"}},
			want:   &Actor{Source: SourceAct, Issuer: "https://token-iss", Subject: "agent-1"},
		},
		{
			name:   "explicit act source",
			source: SourceAct,
			claims: map[string]any{"act": map[string]any{"iss": "https://platform", "sub": "agent-1"}},
			want:   &Actor{Source: SourceAct, Issuer: "https://platform", Subject: "agent-1"},
		},
		{name: "act that is nil", claims: map[string]any{"act": nil}, wantErr: ErrNoActor},
		{
			name:    "act.iss that is empty",
			claims:  map[string]any{"act": map[string]any{"iss": "", "sub": "agent-1"}},
			wantErr: ErrMalformedActor,
		},
		{
			name:     "client source without a token issuer",
			source:   SourceClient,
			client:   "client-1",
			noIssuer: true,
			wantErr:  ErrMalformedActor,
		},
		{name: "no act is refused", claims: map[string]any{"sub": "u"}, wantErr: ErrNoActor},
		{name: "act that is not an object", claims: map[string]any{"act": "agent-1"}, wantErr: ErrMalformedActor},
		{name: "act without sub", claims: map[string]any{"act": map[string]any{"iss": "https://platform"}}, wantErr: ErrMalformedActor},
		{
			name:   "client source ignores act and names the client",
			source: SourceClient,
			claims: map[string]any{"act": map[string]any{"sub": "agent-1"}},
			client: "client-1",
			want:   &Actor{Source: SourceClient, Issuer: "https://token-iss", Subject: "client-1"},
		},
		{name: "client source without a client ID", source: SourceClient, wantErr: ErrMalformedActor},
		{name: "unknown source", source: "bogus", wantFail: true},
		{
			name:    "act.iss that is not a string",
			claims:  map[string]any{"act": map[string]any{"iss": 42, "sub": "agent-1"}},
			wantErr: ErrMalformedActor,
		},
		{
			name:     "no iss in act or on the token",
			claims:   map[string]any{"act": map[string]any{"sub": "agent-1"}},
			noIssuer: true,
			wantErr:  ErrMalformedActor,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tokenIssuer := "https://token-iss"
			if tt.noIssuer {
				tokenIssuer = ""
			}
			got, err := Effective(tt.source, tt.claims, tokenIssuer, tt.client)
			if tt.wantErr != nil || tt.wantFail {
				require.Error(t, err)
				if tt.wantErr != nil {
					require.ErrorIs(t, err, tt.wantErr)
				}
				assert.Nil(t, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want.Source, got.Source)
			assert.Equal(t, tt.want.Issuer, got.Issuer)
			assert.Equal(t, tt.want.Subject, got.Subject)
			if tt.wantClaims != nil {
				assert.Equal(t, tt.wantClaims, got.Claims)
			}
			if tt.want.Source == SourceAct {
				assert.NotContains(t, got.Claims, "act")
			}
		})
	}
}

func TestNewDefaultPolicy_Refusals(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		tokenIssuer  string
		actorIssuers []string
	}{
		{name: "empty token issuer", actorIssuers: []string{"spiffe://td"}},
		{name: "empty actor issuer", tokenIssuer: "https://as", actorIssuers: []string{""}},
		{name: "token issuer with #", tokenIssuer: "https://as#frag"},
		{name: "actor issuer with #", tokenIssuer: "https://as", actorIssuers: []string{"spiffe://td#frag"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p, err := NewDefaultPolicy(tt.tokenIssuer, tt.actorIssuers)
			require.Error(t, err)
			assert.Nil(t, p)
		})
	}
}

func TestEffective_ClaimsDoNotAliasInput(t *testing.T) {
	t.Parallel()
	nested := map[string]any{"sub": "agent-0"}
	inner := map[string]any{"level": "high"}
	list := []any{"a", map[string]any{"k": "v"}}
	act := map[string]any{"iss": "https://platform", "sub": "agent-1", "act": nested, "meta": inner, "roles": list}
	claims := map[string]any{"act": act}

	got, err := Effective(SourceAct, claims, "https://token-iss", "")
	require.NoError(t, err)
	want := map[string]any{"iss": "https://platform", "sub": "agent-1", "meta": map[string]any{"level": "high"},
		"roles": []any{"a", map[string]any{"k": "v"}}}
	require.Equal(t, want, got.Claims)

	inner["level"] = "tampered"
	list[0] = "tampered"
	list[1].(map[string]any)["k"] = "tampered"

	assert.Equal(t, want, got.Claims)

	got.Claims["sub"] = "tampered"
	got.Claims["extra"] = true
	assert.Equal(t, "agent-1", act["sub"])
	assert.NotContains(t, act, "extra")
}

func TestDefaultPolicy_Evaluate(t *testing.T) {
	t.Parallel()
	const (
		tokenIss = "https://as"
		spiffe   = "spiffe://td"
	)
	tests := []struct {
		name        string
		tokenIssuer string
		actor       *Actor
		wantErr     bool
		wantAllow   bool
		wantReason  string
		wantPrinc   string
		wantSource  string
	}{
		{
			name:        "actor issuer is the token issuer",
			tokenIssuer: tokenIss,
			actor:       &Actor{Issuer: tokenIss, Subject: "agent-1", Source: SourceAct},
			wantAllow:   true,
			wantReason:  ReasonAllowed,
			wantPrinc:   tokenIss + "#agent-1",
			wantSource:  SourceAct,
		},
		{
			name:        "actor from the client",
			tokenIssuer: tokenIss,
			actor:       &Actor{Issuer: tokenIss, Subject: "client-1", Source: SourceClient},
			wantAllow:   true,
			wantReason:  ReasonAllowed,
			wantPrinc:   tokenIss + "#client-1",
			wantSource:  SourceClient,
		},
		{
			name:        "actor issuer is a configured actor issuer",
			tokenIssuer: tokenIss,
			actor:       &Actor{Issuer: spiffe, Subject: "ns/agent"},
			wantAllow:   true,
			wantReason:  ReasonAllowed,
			wantPrinc:   spiffe + "#ns/agent",
		},
		{
			name:        "actor issuer not allowed",
			tokenIssuer: tokenIss,
			actor:       &Actor{Issuer: "https://evil", Subject: "agent-1", Source: SourceAct},
			wantReason:  ReasonIssuerNotAllowed,
			wantSource:  SourceAct,
		},
		{name: "no actor", tokenIssuer: tokenIss, wantReason: ReasonNoActor},
		{
			name:        "actor without subject",
			tokenIssuer: tokenIss,
			actor:       &Actor{Issuer: spiffe},
			wantReason:  ReasonNoActor,
		},
		{
			name:        "actor without issuer",
			tokenIssuer: tokenIss,
			actor:       &Actor{Subject: "agent-1"},
			wantReason:  ReasonNoActor,
		},
		{
			name:        "request for another token issuer",
			tokenIssuer: "https://other-as",
			actor:       &Actor{Issuer: spiffe, Subject: "ns/agent"},
			wantErr:     true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p, err := NewDefaultPolicy(tokenIss, []string{spiffe})
			require.NoError(t, err)
			got, err := p.Evaluate(context.Background(), Request{TokenIssuer: tt.tokenIssuer, Actor: tt.actor})
			if tt.wantErr {
				require.Error(t, err)
				assert.NotContains(t, err.Error(), tt.tokenIssuer)
				assert.Contains(t, err.Error(), tokenIss)
				assert.False(t, got.Allow)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, Decision{Allow: tt.wantAllow, Reason: tt.wantReason, Principal: tt.wantPrinc, Source: tt.wantSource}, got)
		})
	}
}

func TestReasons(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		ReasonAllowed:          "allowed",
		ReasonNoActor:          "no_actor",
		ReasonIssuerNotAllowed: "actor_issuer_not_allowed",
	}
	for got, want := range tests {
		assert.Equal(t, want, got)
	}
}
