// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authorizationdetails

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const grantTypeRedemption = "urn:ietf:params:oauth:grant-type:jwt-bearer"

func TestPending_GrantSubset(t *testing.T) {
	t.Parallel()

	keepAll := func(req Request) (Result, error) { return Result{Entries: req.Entries}, nil }
	dropAll := func(Request) (Result, error) { return Result{}, nil }
	returns := func(entries ...string) func(Request) (Result, error) {
		return func(Request) (Result, error) {
			out := make([]json.RawMessage, len(entries))
			for i, entry := range entries {
				out[i] = json.RawMessage(entry)
			}
			return Result{Entries: out}, nil
		}
	}
	rejection := Error{Code: CodeInvalidAuthorizationDetails, Reason: "not_allowed"}

	tests := []struct {
		name        string
		raw         string
		hooks       map[string]func(Request) (Result, error)
		want        string
		wantDropped []string
		check       func(t *testing.T, err error)
	}{
		{
			name:  "every entry kept, in parsed order and grouped by type",
			raw:   `[{"type":"a","n":1},{"type":"b","n":2},{"type":"a","n":3}]`,
			hooks: map[string]func(Request) (Result, error){"a": keepAll, "b": keepAll},
			want:  `[{"type":"a","n":1},{"type":"a","n":3},{"type":"b","n":2}]`,
		},
		{
			name:  "entries returned in another order keep their parsed order",
			raw:   `[{"type":"a","n":1},{"type":"a","n":2}]`,
			hooks: map[string]func(Request) (Result, error){"a": returns(`{"type":"a","n":2}`, `{"type":"a","n":1}`)},
			want:  `[{"type":"a","n":1},{"type":"a","n":2}]`,
		},
		{
			name:  "one entry of a type dropped",
			raw:   `[{"type":"a","n":1},{"type":"a","n":2}]`,
			hooks: map[string]func(Request) (Result, error){"a": returns(`{"type":"a","n":2}`)},
			want:  `[{"type":"a","n":2}]`,
		},
		{
			name:        "a type that keeps none is dropped and the other type's entry is kept",
			raw:         `[{"type":"a","n":1},{"type":"b","n":2}]`,
			hooks:       map[string]func(Request) (Result, error){"a": dropAll, "b": keepAll},
			want:        `[{"type":"b","n":2}]`,
			wantDropped: []string{"a"},
		},
		{
			name:        "every type dropped leaves no entry",
			raw:         `[{"type":"a","n":1},{"type":"b","n":2}]`,
			hooks:       map[string]func(Request) (Result, error){"a": dropAll, "b": dropAll},
			wantDropped: []string{"a", "b"},
		},
		{
			name:  "the parsed bytes are kept, not the hook's",
			raw:   `[{ "type" : "a", "n" : 1, "s" : "<A>" }]`,
			hooks: map[string]func(Request) (Result, error){"a": returns(`{"s":"<A>","n":1,"type":"a"}`)},
			want:  `[{ "type" : "a", "n" : 1, "s" : "<A>" }]`,
		},
		{
			name:  "a repeated entry may be kept twice",
			raw:   `[{"type":"a"},{"type":"a"}]`,
			hooks: map[string]func(Request) (Result, error){"a": keepAll},
			want:  `[{"type":"a"},{"type":"a"}]`,
		},
		{
			name:  "an entry kept more often than parsed is refused",
			raw:   `[{"type":"a"}]`,
			hooks: map[string]func(Request) (Result, error){"a": returns(`{"type":"a"}`, `{"type":"a"}`)},
			check: requireErrorIs(errInvalidHookResult),
		},
		{
			name:  "a changed member is refused",
			raw:   `[{"type":"a","n":1}]`,
			hooks: map[string]func(Request) (Result, error){"a": returns(`{"type":"a","n":2}`)},
			check: requireErrorIs(errInvalidHookResult),
		},
		{
			name:  "an added member is refused",
			raw:   `[{"type":"a","n":1}]`,
			hooks: map[string]func(Request) (Result, error){"a": returns(`{"type":"a","n":1,"x":1}`)},
			check: requireErrorIs(errInvalidHookResult),
		},
		{
			name:  "a number written differently is refused",
			raw:   `[{"type":"a","n":1}]`,
			hooks: map[string]func(Request) (Result, error){"a": returns(`{"type":"a","n":1.0}`)},
			check: requireErrorIs(errInvalidHookResult),
		},
		{
			name:  "a rounded big integer is refused",
			raw:   `[{"type":"a","n":9007199254740993}]`,
			hooks: map[string]func(Request) (Result, error){"a": returns(`{"type":"a","n":9007199254740992}`)},
			check: requireErrorIs(errInvalidHookResult),
		},
		{
			name:  "another type's entry is refused",
			raw:   `[{"type":"a"},{"type":"b"}]`,
			hooks: map[string]func(Request) (Result, error){"a": returns(`{"type":"b"}`), "b": keepAll},
			check: requireErrorIs(errInvalidHookResult),
		},
		{
			name:  "trailing data after an entry is refused",
			raw:   `[{"type":"a"}]`,
			hooks: map[string]func(Request) (Result, error){"a": returns(`{"type":"a"} {}`)},
			check: requireErrorIs(errInvalidHookResult),
		},
		{
			name:  "an oversized entry is refused",
			raw:   `[{"type":"a"}]`,
			hooks: map[string]func(Request) (Result, error){"a": returns(sizedEntry(maxSize + 1))},
			check: requireErrorIs(errInvalidHookResult),
		},
		{
			name:  "a typed hook error is passed through",
			raw:   `[{"type":"a"}]`,
			hooks: map[string]func(Request) (Result, error){"a": func(Request) (Result, error) { return Result{}, rejection }},
			check: requireTypedError(rejection),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			types := map[string]Type{}
			for name, hook := range tt.hooks {
				types[name] = Type{
					GrantTypes: []string{grantTypeRedemption},
					Grant:      func(_ context.Context, req Request) (Result, error) { return hook(req) },
				}
			}
			registry, err := New(types)
			require.NoError(t, err)
			pending, err := registry.Parse(tt.raw, grantTypeRedemption)
			require.NoError(t, err)

			got, err := pending.GrantSubset(context.Background(), Request{ClientID: "client"})
			if tt.check != nil {
				require.Error(t, err)
				assert.Zero(t, got)
				tt.check(t, err)
				return
			}
			require.NoError(t, err)
			if tt.want == "" {
				assert.Nil(t, got.Entries)
			} else {
				assert.Equal(t, tt.want, string(got.Entries))
			}
			assert.Equal(t, tt.wantDropped, got.Dropped)
		})
	}
}

// TestPending_GrantSubset_LoneSurrogate pins that a hook's entry that equals
// a parsed one only after decoding still yields the parsed bytes: a lone
// surrogate escape decodes to U+FFFD, the same as the hook's literal U+FFFD.
func TestPending_GrantSubset_LoneSurrogate(t *testing.T) {
	t.Parallel()

	const parsed = `{"type":"a","s":"\ud800"}`
	registry, err := New(map[string]Type{"a": {
		GrantTypes: []string{grantTypeRedemption},
		Grant: func(context.Context, Request) (Result, error) {
			return Result{Entries: []json.RawMessage{json.RawMessage(`{"type":"a","s":"�"}`)}}, nil
		},
	}})
	require.NoError(t, err)
	pending, err := registry.Parse("["+parsed+"]", grantTypeRedemption)
	require.NoError(t, err)

	got, err := pending.GrantSubset(context.Background(), Request{})
	require.NoError(t, err)
	assert.Equal(t, "["+parsed+"]", string(got.Entries))
}

// TestPending_GrantSubset_HookCannotChangeParsed pins that a hook that
// modifies the entries it received in place does not change what is kept.
func TestPending_GrantSubset_HookCannotChangeParsed(t *testing.T) {
	t.Parallel()

	const parsed = `{"type":"a","s":"x"}`
	registry, err := New(map[string]Type{"a": {
		GrantTypes: []string{grantTypeRedemption},
		Grant: func(_ context.Context, req Request) (Result, error) {
			kept := cloneEntries(req.Entries)
			for _, entry := range req.Entries {
				copy(entry, strings.Repeat(" ", len(entry)))
			}
			return Result{Entries: kept}, nil
		},
	}})
	require.NoError(t, err)
	pending, err := registry.Parse("["+parsed+"]", grantTypeRedemption)
	require.NoError(t, err)

	got, err := pending.GrantSubset(context.Background(), Request{})
	require.NoError(t, err)
	assert.Equal(t, "["+parsed+"]", string(got.Entries))
}

// TestPending_GrantSubset_PlainHookErrorIsInternal pins that an untyped hook
// error stays an internal failure.
func TestPending_GrantSubset_PlainHookErrorIsInternal(t *testing.T) {
	t.Parallel()

	boom := errors.New("backend unavailable")
	registry, err := New(map[string]Type{"a": {
		GrantTypes: []string{grantTypeRedemption},
		Grant:      func(context.Context, Request) (Result, error) { return Result{}, boom },
	}})
	require.NoError(t, err)
	pending, err := registry.Parse(`[{"type":"a"}]`, grantTypeRedemption)
	require.NoError(t, err)

	_, err = pending.GrantSubset(context.Background(), Request{})
	require.ErrorIs(t, err, boom)
	var typed Error
	assert.False(t, errors.As(err, &typed))
}
