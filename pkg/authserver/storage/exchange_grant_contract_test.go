// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package storage

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/ory/fosite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// exchangeGrantStoreFactory returns a fresh, empty store and a function that
// moves the store's clock forward by the given duration. The contract suite
// measures all time against that clock, so a backend must make expiry follow it.
type exchangeGrantStoreFactory func(t *testing.T) (ExchangeGrantStorage, func(time.Duration))

// contractEpoch is the instant every contract record is built around. A backend
// whose clock starts elsewhere must start it here.
var contractEpoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// contractChild builds a child of the grant whose signature is "parent".
func contractChild(signature string) *ExchangeGrant {
	g := contractGrant(signature)
	g.ParentSignature = "parent"
	g.SourceIssuer = ""
	g.SourceJTI = ""
	g.SourceExpiresAt = time.Time{}
	return g
}

func contractGrant(signature string) *ExchangeGrant {
	return &ExchangeGrant{
		Signature:          signature,
		ClientID:           "client-a",
		Subject:            "user-1",
		SubjectIdentifier:  `{"format":"iss_sub","iss":"https://issuer.example","sub":"user-1"}`,
		SourceIssuer:       "https://issuer.example",
		SourceJTI:          "jti-" + signature,
		SourceExpiresAt:    contractEpoch.Add(10 * time.Minute),
		IssuableTokenTypes: []string{"urn:ietf:params:oauth:token-type:refresh_token"},
		CreatedAt:          contractEpoch,
		ExpiresAt:          contractEpoch.Add(time.Hour),
	}
}

// runExchangeGrantStorageContract checks the ExchangeGrantStorage contract. It
// is backend-agnostic: memory runs it now and Redis reuses it.
func runExchangeGrantStorageContract(t *testing.T, newStore exchangeGrantStoreFactory) {
	t.Helper()
	ctx := context.Background()

	t.Run("create then get returns the record", func(t *testing.T) {
		t.Parallel()
		store, _ := newStore(t)
		g := contractGrant("sig-1")
		require.NoError(t, store.CreateExchangeGrant(ctx, g))

		got, err := store.GetExchangeGrant(ctx, "sig-1")
		require.NoError(t, err)
		assert.Equal(t, g.Signature, got.Signature)
		assert.Equal(t, g.ClientID, got.ClientID)
		assert.Equal(t, g.Subject, got.Subject)
		assert.Equal(t, g.SubjectIdentifier, got.SubjectIdentifier)
		assert.Equal(t, g.SourceIssuer, got.SourceIssuer)
		assert.Equal(t, g.SourceJTI, got.SourceJTI)
		assert.True(t, g.SourceExpiresAt.Equal(got.SourceExpiresAt))
		assert.Equal(t, g.IssuableTokenTypes, got.IssuableTokenTypes)
		assert.True(t, g.ExpiresAt.Equal(got.ExpiresAt))
		assert.True(t, g.CreatedAt.Equal(got.CreatedAt))
		assert.Empty(t, got.ParentSignature)
		assert.True(t, got.RevokedAt.IsZero())
	})

	t.Run("child round-trips its parent signature", func(t *testing.T) {
		t.Parallel()
		store, _ := newStore(t)
		require.NoError(t, store.CreateExchangeGrant(ctx, contractGrant("parent")))
		child := contractChild("child")
		require.NoError(t, store.CreateExchangeGrant(ctx, child))

		got, err := store.GetExchangeGrant(ctx, "child")
		require.NoError(t, err)
		assert.Equal(t, "parent", got.ParentSignature)
		assert.Empty(t, got.SourceJTI)
		assert.True(t, child.CreatedAt.Equal(got.CreatedAt))
	})

	t.Run("malformed shape is refused and writes nothing", func(t *testing.T) {
		t.Parallel()
		cases := []struct {
			name   string
			base   func() *ExchangeGrant // nil means contractGrant("sig-bad")
			mutate func(g *ExchangeGrant)
		}{
			{name: "jti without issuer", mutate: func(g *ExchangeGrant) { g.SourceIssuer = "" }},
			{name: "jti without expiry", mutate: func(g *ExchangeGrant) { g.SourceExpiresAt = time.Time{} }},
			{name: "issuer without jti", mutate: func(g *ExchangeGrant) { g.SourceJTI = "" }},
			{name: "empty signature", mutate: func(g *ExchangeGrant) { g.Signature = "" }},
			{name: "zero ExpiresAt", mutate: func(g *ExchangeGrant) { g.ExpiresAt = time.Time{} }},
			{name: "top-level without source", mutate: func(g *ExchangeGrant) {
				g.SourceIssuer = ""
				g.SourceJTI = ""
				g.SourceExpiresAt = time.Time{}
			}},
			{name: "child with source", mutate: func(g *ExchangeGrant) { g.ParentSignature = "parent" }},
			{
				name: "own parent",
				base: func() *ExchangeGrant { return contractChild("sig-bad") },
				mutate: func(g *ExchangeGrant) {
					g.ParentSignature = "sig-bad"
				},
			},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				store, _ := newStore(t)
				base := tc.base
				if base == nil {
					base = func() *ExchangeGrant { return contractGrant("sig-bad") }
				}
				g := base()
				tc.mutate(g)
				require.Error(t, store.CreateExchangeGrant(ctx, g))

				_, err := store.GetExchangeGrant(ctx, "sig-bad")
				assert.ErrorIs(t, err, ErrNotFound)
			})
		}
	})

	t.Run("already expired grant is refused and burns no jti", func(t *testing.T) {
		t.Parallel()
		store, advance := newStore(t)
		advance(2 * time.Hour) // past contractGrant's ExpiresAt (epoch + 1h)

		g := contractGrant("sig-1")
		g.SourceExpiresAt = contractEpoch.Add(3 * time.Hour) // marker window still open
		require.Error(t, store.CreateExchangeGrant(ctx, g))
		_, err := store.GetExchangeGrant(ctx, "sig-1")
		assert.ErrorIs(t, err, ErrNotFound)

		// The failed create must not have consumed the jti.
		ok := contractGrant("sig-2")
		ok.SourceJTI = g.SourceJTI
		ok.SourceExpiresAt = g.SourceExpiresAt
		ok.ExpiresAt = contractEpoch.Add(4 * time.Hour)
		require.NoError(t, store.CreateExchangeGrant(ctx, ok))
	})

	t.Run("revoke with a zero time is an error", func(t *testing.T) {
		t.Parallel()
		store, _ := newStore(t)
		require.NoError(t, store.CreateExchangeGrant(ctx, contractGrant("sig-1")))
		require.Error(t, store.RevokeExchangeGrant(ctx, "sig-1", time.Time{}))
		got, err := store.GetExchangeGrant(ctx, "sig-1")
		require.NoError(t, err)
		assert.True(t, got.RevokedAt.IsZero())
	})

	t.Run("get of a missing grant is not found", func(t *testing.T) {
		t.Parallel()
		store, _ := newStore(t)
		_, err := store.GetExchangeGrant(ctx, "missing")
		assert.ErrorIs(t, err, ErrNotFound)
	})

	t.Run("duplicate signature is refused and leaves no jti marker", func(t *testing.T) {
		t.Parallel()
		store, _ := newStore(t)
		require.NoError(t, store.CreateExchangeGrant(ctx, contractGrant("sig-1")))

		dup := contractGrant("sig-1")
		dup.SourceJTI = "another-jti"
		require.ErrorIs(t, store.CreateExchangeGrant(ctx, dup), ErrAlreadyExists)

		// The failed create must not have consumed "another-jti".
		other := contractGrant("sig-2")
		other.SourceJTI = "another-jti"
		require.NoError(t, store.CreateExchangeGrant(ctx, other))
	})

	t.Run("duplicate signature is reported before a reused jti", func(t *testing.T) {
		t.Parallel()
		store, _ := newStore(t)
		g := contractGrant("sig-1")
		require.NoError(t, store.CreateExchangeGrant(ctx, g))

		// Same signature and same jti: the signature check wins.
		err := store.CreateExchangeGrant(ctx, contractGrant("sig-1"))
		require.ErrorIs(t, err, ErrAlreadyExists)
		assert.NotErrorIs(t, err, fosite.ErrJTIKnown)
	})

	t.Run("reused jti is refused and writes nothing", func(t *testing.T) {
		t.Parallel()
		store, _ := newStore(t)
		require.NoError(t, store.CreateExchangeGrant(ctx, contractGrant("sig-1")))

		reuse := contractGrant("sig-2")
		reuse.SourceJTI = "jti-sig-1"
		require.ErrorIs(t, store.CreateExchangeGrant(ctx, reuse), fosite.ErrJTIKnown)

		_, err := store.GetExchangeGrant(ctx, "sig-2")
		assert.ErrorIs(t, err, ErrNotFound)
	})

	t.Run("jti is scoped by issuer", func(t *testing.T) {
		t.Parallel()
		store, _ := newStore(t)
		require.NoError(t, store.CreateExchangeGrant(ctx, contractGrant("sig-1")))

		other := contractGrant("sig-2")
		other.SourceJTI = "jti-sig-1"
		other.SourceIssuer = "https://other-issuer.example"
		require.NoError(t, store.CreateExchangeGrant(ctx, other))
	})

	t.Run("jti marker lasts until SourceExpiresAt plus the margin, inclusive", func(t *testing.T) {
		t.Parallel()
		store, advance := newStore(t)
		require.NoError(t, store.CreateExchangeGrant(ctx, contractGrant("sig-1")))
		reuse := contractGrant("sig-2")
		reuse.SourceJTI = "jti-sig-1"

		advance(10 * time.Minute) // exactly SourceExpiresAt
		require.ErrorIs(t, store.CreateExchangeGrant(ctx, reuse), fosite.ErrJTIKnown)

		advance(ExchangeGrantMarkerMargin) // exactly SourceExpiresAt + margin
		require.ErrorIs(t, store.CreateExchangeGrant(ctx, reuse), fosite.ErrJTIKnown)

		advance(time.Second) // just past it
		require.NoError(t, store.CreateExchangeGrant(ctx, reuse))
	})

	t.Run("child with empty SourceJTI writes no marker", func(t *testing.T) {
		t.Parallel()
		store, _ := newStore(t)
		require.NoError(t, store.CreateExchangeGrant(ctx, contractGrant("parent")))

		for _, sig := range []string{"child-1", "child-2"} {
			require.NoError(t, store.CreateExchangeGrant(ctx, contractChild(sig)), sig)
		}
	})

	t.Run("concurrent creates with one jti admit exactly one", func(t *testing.T) {
		t.Parallel()
		store, _ := newStore(t)
		const workers = 50

		var ready, done sync.WaitGroup
		start := make(chan struct{})
		var startOnce sync.Once
		release := func() { startOnce.Do(func() { close(start) }) }
		defer release() // frees workers if the barrier times out
		errs := make([]error, workers)
		ready.Add(workers)
		done.Add(workers)
		for i := 0; i < workers; i++ {
			go func() {
				defer done.Done()
				g := contractGrant(fmt.Sprintf("sig-%d", i))
				g.SourceJTI = "shared-jti"
				ready.Done()
				<-start
				errs[i] = store.CreateExchangeGrant(ctx, g)
			}()
		}

		waitOrFail(t, &ready, "workers did not reach the barrier")
		release()
		waitOrFail(t, &done, "creates did not finish")

		var ok, known int
		for _, err := range errs {
			switch {
			case err == nil:
				ok++
			case errors.Is(err, fosite.ErrJTIKnown):
				known++
			default:
				t.Errorf("unexpected error: %v", err)
			}
		}
		assert.Equal(t, 1, ok)
		assert.Equal(t, workers-1, known)
	})

	t.Run("revoked grants are returned with RevokedAt set", func(t *testing.T) {
		t.Parallel()
		store, _ := newStore(t)
		require.NoError(t, store.CreateExchangeGrant(ctx, contractGrant("sig-1")))
		revokedAt := contractEpoch.Add(time.Minute)
		require.NoError(t, store.RevokeExchangeGrant(ctx, "sig-1", revokedAt))

		got, err := store.GetExchangeGrant(ctx, "sig-1")
		require.NoError(t, err)
		assert.True(t, revokedAt.Equal(got.RevokedAt))
		assert.ErrorIs(t, got.Live(revokedAt), ErrRevoked)
	})

	t.Run("revoke is idempotent and keeps the first RevokedAt", func(t *testing.T) {
		t.Parallel()
		store, _ := newStore(t)
		require.NoError(t, store.CreateExchangeGrant(ctx, contractGrant("sig-1")))
		first := contractEpoch.Add(time.Minute)
		require.NoError(t, store.RevokeExchangeGrant(ctx, "sig-1", first))
		require.NoError(t, store.RevokeExchangeGrant(ctx, "sig-1", first.Add(time.Hour)))

		got, err := store.GetExchangeGrant(ctx, "sig-1")
		require.NoError(t, err)
		assert.True(t, first.Equal(got.RevokedAt))
	})

	t.Run("revoke of a missing grant is not found", func(t *testing.T) {
		t.Parallel()
		store, _ := newStore(t)
		err := store.RevokeExchangeGrant(ctx, "missing", contractEpoch)
		assert.ErrorIs(t, err, ErrNotFound)
	})

	t.Run("get is not found after ExpiresAt, revoked or not", func(t *testing.T) {
		t.Parallel()
		store, advance := newStore(t)
		require.NoError(t, store.CreateExchangeGrant(ctx, contractGrant("live")))
		require.NoError(t, store.CreateExchangeGrant(ctx, contractGrant("revoked")))
		require.NoError(t, store.RevokeExchangeGrant(ctx, "revoked", contractEpoch))

		advance(time.Hour)
		_, err := store.GetExchangeGrant(ctx, "live")
		require.NoError(t, err, "a grant is still readable at ExpiresAt")

		advance(time.Second)
		for _, sig := range []string{"live", "revoked"} {
			_, err := store.GetExchangeGrant(ctx, sig)
			assert.ErrorIs(t, err, ErrNotFound, sig)
		}
		assert.ErrorIs(t, store.RevokeExchangeGrant(ctx, "live", contractEpoch.Add(2*time.Hour)), ErrNotFound)
	})

	t.Run("returned and stored records are copies", func(t *testing.T) {
		t.Parallel()
		store, _ := newStore(t)
		in := contractGrant("sig-1")
		require.NoError(t, store.CreateExchangeGrant(ctx, in))

		// Mutating the input after create must not change the store.
		in.IssuableTokenTypes[0] = "mutated-input"
		in.ClientID = "mutated-input"

		got, err := store.GetExchangeGrant(ctx, "sig-1")
		require.NoError(t, err)
		assert.Equal(t, []string{"urn:ietf:params:oauth:token-type:refresh_token"}, got.IssuableTokenTypes)
		assert.Equal(t, "client-a", got.ClientID)

		// Mutating a returned record must not change the store either.
		got.IssuableTokenTypes[0] = "mutated-output"
		got.RevokedAt = contractEpoch
		got.ClientID = "mutated-output"

		again, err := store.GetExchangeGrant(ctx, "sig-1")
		require.NoError(t, err)
		assert.Equal(t, []string{"urn:ietf:params:oauth:token-type:refresh_token"}, again.IssuableTokenTypes)
		assert.True(t, again.RevokedAt.IsZero())
		assert.Equal(t, "client-a", again.ClientID)
	})
}

// waitOrFail waits for wg and fails the test if it does not finish in time, so
// a stuck goroutine cannot hang the suite.
func waitOrFail(t *testing.T, wg *sync.WaitGroup, msg string) {
	t.Helper()
	finished := make(chan struct{})
	go func() {
		wg.Wait()
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(10 * time.Second):
		t.Fatal(msg)
	}
}
