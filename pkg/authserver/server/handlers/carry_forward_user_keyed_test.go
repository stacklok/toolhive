// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"context"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	serveridentity "github.com/stacklok/toolhive/pkg/authserver/identity"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/authserver/upstream"
)

// betweenAction is what happens to the user row between the login's read and
// its write: the row is deleted, or another client stores a row with rt.
type betweenAction struct {
	deleteRow bool
	rt, at    string
}

// TestRefreshTokenCarryForward_UserKeyedStorage covers the login paths that
// write the shared per-user row (browser callback and device confirmation)
// when the IdP returns no refresh token. The handler's storage is the real
// user-keyed wrapper over the shared mock backend, so a read that bypasses the
// wrapper would see the legacy session-keyed row and fail the case.
func TestRefreshTokenCarryForward_UserKeyedStorage(t *testing.T) {
	t.Parallel()

	const (
		providerName = "test-upstream"
		userID       = "user-id"
		upstreamSub  = "user-123"
	)

	type seed struct {
		sharedRT    string // refresh token of the user row; "" = no user row
		legacyRT    string // refresh token of a pre-opt-in session-keyed row; "" = none
		idpRT       string
		userKeyed   bool
		priorSub    string                 // upstream subject of the shared row; "" = the login's
		synthetic   bool                   // the login's provider mints a fresh subject every flow
		readErrs    map[int]error          // errors returned by the Nth read of the user row (1-based)
		foreign     bool                   // the shared row is owned by another user
		between     *betweenAction         // happens once between the read and the write
		readSteps   map[int]*betweenAction // happens after the Nth read of the user row (1-based)
		casSteps    map[int]*betweenAction // happens after the Nth compare-and-swap (1-based)
		conflict    bool                   // the failed login is an upstream account conflict
		expectNoRow bool                   // the failed login leaves no user row
		emptyRow    bool                   // the user row exists without a refresh token
		winnerSub   string                 // upstream subject of the row written between the read and the write; "" = the login's
		retry       bool                   // after the first login, the user signs in again
		expectFail  bool                   // the first login fails and keeps the winning row
		expectedRT  string
		expectedAT  string
		expectedSub string   // upstream subject of the final row; "" = the login's
		flows       []string // flows the row runs for; nil = browser only
		description string
	}

	both := []string{"browser", "device"}
	deviceOnly := []string{"device"}
	rotate := &betweenAction{rt: "rt-rotated", at: "rotated-access"}
	appear := &betweenAction{rt: "rt-appeared", at: "appeared-access"}
	deleteRow := &betweenAction{deleteRow: true}
	second := &betweenAction{rt: "rt-second", at: "second-access"}
	third := &betweenAction{rt: "rt-third", at: "third-access"}

	cases := []seed{
		{
			description: "legacy row with a later expiry is never copied onto the shared row",
			sharedRT:    "rt-current", legacyRT: "rt-legacy", userKeyed: true, flows: both,
			expectedRT: "rt-current", expectedAT: "new-access-token",
		},
		{
			description: "shared row rotated between read and write keeps its refresh token and takes the login's access token",
			sharedRT:    "rt-current", between: rotate, userKeyed: true,
			expectedRT: "rt-rotated", expectedAT: "new-access-token",
		},
		{
			description: "a refresh token from the IdP is stored as is",
			sharedRT:    "rt-current", idpRT: "rt-fresh", userKeyed: true,
			expectedRT: "rt-fresh", expectedAT: "new-access-token",
		},
		{
			description: "no prior row stores without a refresh token",
			userKeyed:   true,
			expectedRT:  "", expectedAT: "new-access-token",
		},
		{
			description: "field off still copies from the latest row of any session",
			legacyRT:    "rt-legacy", flows: deviceOnly,
			expectedRT: "rt-legacy", expectedAT: "new-access-token",
		},
	}

	cases = append(cases,
		seed{
			description: "an expired prior row still donates its refresh token",
			sharedRT:    "rt-current", readErrs: map[int]error{1: storage.ErrExpired}, userKeyed: true,
			expectedRT: "rt-current", expectedAT: "new-access-token",
		},
		seed{
			description: "a failed read keeps the shared row's refresh token and merges the login",
			sharedRT:    "rt-current", readErrs: map[int]error{1: errors.New("storage down")}, userKeyed: true,
			expectedRT: "rt-current", expectedAT: "new-access-token",
		},
		seed{
			description: "a failed read with no row stores the login",
			readErrs:    map[int]error{1: errors.New("storage down")}, userKeyed: true,
			expectedRT: "", expectedAT: "new-access-token",
		},
		seed{
			description: "a row owned by another user is replaced without its refresh token",
			sharedRT:    "rt-foreign", foreign: true, userKeyed: true,
			expectedRT: "", expectedAT: "new-access-token",
		},
		seed{
			description: "row deleted between read and write stores the login without a refresh token",
			sharedRT:    "rt-current", between: deleteRow, userKeyed: true,
			expectedRT: "", expectedAT: "new-access-token",
		},
		seed{
			description: "row appearing with a refresh token after a not-found read keeps that token",
			between:     appear, userKeyed: true,
			expectedRT: "rt-appeared", expectedAT: "new-access-token",
		},
		seed{
			description: "refresh token stored after a read of a row without one keeps that token",
			emptyRow:    true, between: appear, userKeyed: true,
			expectedRT: "rt-appeared", expectedAT: "new-access-token",
		},
		seed{
			description: "prior row without a refresh token is replaced by the login",
			emptyRow:    true, userKeyed: true,
			expectedRT: "", expectedAT: "new-access-token",
		},
		seed{
			description: "prior row of a different upstream subject is replaced without its refresh token",
			sharedRT:    "rt-current", priorSub: "someone-else", userKeyed: true,
			expectedRT: "", expectedAT: "new-access-token",
		},
		seed{
			description: "synthetic provider carries the refresh token despite a different subject",
			sharedRT:    "rt-current", priorSub: "tk-oldrotatingsubject", synthetic: true, userKeyed: true, flows: both,
			expectedRT: "rt-current", expectedAT: "new-access-token",
		},
		seed{
			description: "a different-account winner of a lost carry-forward write is kept and the login fails",
			sharedRT:    "rt-current", between: rotate, winnerSub: "someone-else", userKeyed: true, flows: both,
			expectFail: true, conflict: true,
			expectedRT: "rt-rotated", expectedAT: "rotated-access", expectedSub: "someone-else",
		},
		seed{
			description: "an unreadable winner of a lost carry-forward write fails the login",
			sharedRT:    "rt-current", between: rotate, readErrs: map[int]error{2: errors.New("storage down")}, userKeyed: true,
			expectFail: true,
			expectedRT: "rt-rotated", expectedAT: "rotated-access",
		},
		seed{
			description: "a different-account winner of a lost write after a not-found read fails the login",
			between:     appear, winnerSub: "someone-else", userKeyed: true,
			expectFail: true, conflict: true,
			expectedRT: "rt-appeared", expectedAT: "appeared-access", expectedSub: "someone-else",
		},
		seed{
			description: "an unreadable winner of a lost write over a row without a refresh token fails the login",
			emptyRow:    true, between: appear, readErrs: map[int]error{2: errors.New("storage down")}, userKeyed: true,
			expectFail: true,
			expectedRT: "rt-appeared", expectedAT: "appeared-access",
		},
		seed{
			description: "a synthetic login that lost a write merges into the winner and succeeds",
			sharedRT:    "rt-current", between: rotate, winnerSub: "tk-otherrotatingsubject", synthetic: true,
			userKeyed:  true,
			expectedRT: "rt-rotated", expectedAT: "new-access-token", expectedSub: "tk-newrotatingsubject",
		},
		seed{
			description: "a different-account prior row lost to a write that stored a refresh token keeps that token",
			sharedRT:    "rt-current", priorSub: "someone-else", between: rotate, userKeyed: true, flows: both,
			expectedRT: "rt-rotated", expectedAT: "new-access-token",
		},
		seed{
			description: "a different-account prior row lost to another account's write fails the login",
			sharedRT:    "rt-current", priorSub: "someone-else", between: rotate, winnerSub: "third-account",
			userKeyed: true, flows: both, expectFail: true, conflict: true,
			expectedRT: "rt-rotated", expectedAT: "rotated-access", expectedSub: "third-account",
		},
		seed{
			description: "row deleted between a lost swap and the re-read is written by a retry",
			between:     appear, casSteps: map[int]*betweenAction{1: deleteRow}, userKeyed: true, flows: both,
			expectedRT: "", expectedAT: "new-access-token",
		},
		seed{
			description: "a retry that loses again merges into the new winner",
			between:     appear, casSteps: map[int]*betweenAction{1: deleteRow},
			readSteps: map[int]*betweenAction{2: second}, userKeyed: true, flows: both,
			expectedRT: "rt-second", expectedAT: "new-access-token",
		},
		seed{
			description: "a nil row returned with ErrExpired at the re-read fails the login",
			between:     appear, casSteps: map[int]*betweenAction{1: deleteRow},
			readErrs: map[int]error{2: storage.ErrExpired}, userKeyed: true, flows: both,
			expectFail: true, expectNoRow: true,
		},
		seed{
			description: "a merge that loses keeps the newer winner and succeeds",
			sharedRT:    "rt-current", between: rotate, readSteps: map[int]*betweenAction{2: third},
			userKeyed: true, flows: both,
			expectedRT: "rt-third", expectedAT: "third-access",
		},
		seed{
			description: "a retry after a failed login replaces the other account's row",
			sharedRT:    "rt-current", between: rotate, winnerSub: "someone-else", retry: true, userKeyed: true,
			expectFail: true, conflict: true,
			expectedRT: "", expectedAT: "new-access-token",
		},
	)

	for _, flow := range both {
		for _, tc := range cases {
			if wantFlows := tc.flows; (wantFlows == nil && flow != "browser") ||
				(wantFlows != nil && !slices.Contains(wantFlows, flow)) {
				continue
			}
			t.Run(flow+"/"+tc.description, func(t *testing.T) {
				t.Parallel()

				provider, oauth2Config, stor, storState := baseTestSetup(t, withUserKeyedUpstreamMocks())
				idpSubject := upstreamSub
				if tc.synthetic {
					idpSubject = "tk-newrotatingsubject"
				}
				mockUpstream := &mockIDPProvider{
					providerType:     upstream.ProviderTypeOAuth2,
					authorizationURL: "https://idp.example.com/authorize",
					exchangeResult: &upstream.Identity{
						Tokens: &upstream.Tokens{
							AccessToken:  "new-access-token",
							RefreshToken: tc.idpRT,
							IDToken:      "new-id-token",
							ExpiresAt:    time.Now().Add(time.Hour),
						},
						Subject:   idpSubject,
						Synthetic: tc.synthetic,
					},
				}
				upstreams := []NamedUpstream{{Name: providerName, Provider: mockUpstream}}
				var wrapped storage.Storage = stor
				if tc.userKeyed {
					wrapper, wrapErr := storage.NewUserKeyedUpstreamStorage(stor)
					require.NoError(t, wrapErr)
					wrapped = wrapper
				}
				handler, err := NewHandler(provider, oauth2Config, wrapped, upstreams)
				require.NoError(t, err)

				userCtx := serveridentity.WithCanonicalUser(context.Background(), userID)
				row := func(rt, at string, ttl time.Duration) *storage.UpstreamTokens {
					return &storage.UpstreamTokens{
						ProviderID: providerName, AccessToken: at, RefreshToken: rt,
						ExpiresAt: time.Now().Add(ttl), ClientID: testAuthClientID,
						UserID: userID, UpstreamSubject: upstreamSub,
					}
				}
				winnerRow := func(rt, at string) *storage.UpstreamTokens {
					r := row(rt, at, 20*time.Minute)
					if tc.winnerSub != "" {
						r.UpstreamSubject = tc.winnerSub
					}
					return r
				}
				if tc.sharedRT != "" {
					shared := row(tc.sharedRT, "old-access", 10*time.Minute)
					if tc.priorSub != "" {
						shared.UpstreamSubject = tc.priorSub
					}
					require.NoError(t, wrapped.StoreUpstreamTokens(userCtx, "any-session", providerName, shared))
				}
				if tc.emptyRow {
					require.NoError(t, wrapped.StoreUpstreamTokens(userCtx, "any-session", providerName,
						row("", "old-access", 10*time.Minute)))
				}
				if tc.foreign {
					for _, r := range storState.upstreamTokens {
						r.UserID = "another-user"
					}
				}
				if tc.readErrs != nil {
					storState.getUpstreamErrs = maps.Clone(tc.readErrs)
				}
				if tc.legacyRT != "" {
					storState.upstreamTokens["old-tsid:"+providerName] = row(tc.legacyRT, "legacy-access", time.Hour)
				}
				apply := func(b *betweenAction) {
					if b.deleteRow {
						for k := range storState.upstreamTokens {
							delete(storState.upstreamTokens, k)
						}
						return
					}
					require.NoError(t, wrapped.StoreUpstreamTokens(userCtx, "any-session", providerName,
						winnerRow(b.rt, b.at)))
				}
				if b := tc.between; b != nil {
					done := false
					storState.afterGetUpstream = func() {
						if done {
							return
						}
						done = true
						apply(b)
					}
				}
				storState.afterGetUpstreamCalls = map[int]func(){}
				for n, b := range tc.readSteps {
					storState.afterGetUpstreamCalls[n] = func() { apply(b) }
				}
				storState.afterCASCalls = map[int]func(){}
				for n, b := range tc.casSteps {
					storState.afterCASCalls[n] = func() { apply(b) }
				}

				const newSessionID = "new-session"
				// login runs one sign-in and reports whether it failed.
				login := func() (failed bool) {
					switch flow {
					case "browser":
						storState.users[userID] = &storage.User{ID: userID, CreatedAt: time.Now(), UpdatedAt: time.Now()}
						storState.providerIdentities[providerName+":"+upstreamSub] = &storage.ProviderIdentity{
							UserID: userID, ProviderID: providerName, ProviderSubject: upstreamSub,
							LinkedAt: time.Now(), LastUsedAt: time.Now(),
						}
						internalState := testInternalState
						pending := &storage.PendingAuthorization{
							ClientID: testAuthClientID, RedirectURI: testAuthRedirectURI, State: "client-state",
							PKCEChallenge: "challenge123", PKCEMethod: "S256", Scopes: []string{"openid"},
							InternalState:        internalState,
							UpstreamPKCEVerifier: "verifier-1234567890123456789012345678",
							SessionID:            newSessionID, UpstreamProviderName: providerName, CreatedAt: time.Now(),
						}
						if tc.synthetic {
							// A synthetic upstream is a later leg: the user comes from the first leg.
							pending.ResolvedUserID = userID
							pending.ResolvedUserName = "First Leg User"
							pending.ResolvedUserEmail = "firstleg@example.com"
							pending.ChainUpstreams = []string{providerName}
						}
						cookie := bindPending(t, storState, internalState, pending)
						rec := httptest.NewRecorder()
						handler.CallbackHandler(rec, newCallbackRequest("code=test-code&state="+internalState, cookie))
						require.Equal(t, http.StatusSeeOther, rec.Code)
						loc, parseErr := url.Parse(rec.Header().Get("Location"))
						require.NoError(t, parseErr)
						q := loc.Query()
						if q.Get("error") != "" {
							assert.Equal(t, "server_error", q.Get("error"))
							assert.Empty(t, q.Get("code"))
							return true
						}
						assert.NotEmpty(t, q.Get("code"))
						return false
					default:
						err := handler.persistDeviceSessionTokens(context.Background(),
							&storage.PendingDeviceConfirmation{
								ResolvedUserID: userID,
								Synthetic:      tc.synthetic,
								UpstreamTokens: &storage.UpstreamTokens{
									ProviderID: providerName, AccessToken: "new-access-token", RefreshToken: tc.idpRT,
									IDToken: "new-id-token", ExpiresAt: time.Now().Add(time.Hour),
									ClientID: testAuthClientID, UpstreamSubject: idpSubject,
								},
							}, newSessionID)
						if err != nil && tc.conflict {
							assert.ErrorIs(t, err, errUpstreamAccountConflict)
						} else if err != nil {
							assert.NotErrorIs(t, err, errUpstreamAccountConflict)
						}
						return err != nil
					}
				}
				require.Equal(t, tc.expectFail, login(), "first login outcome")
				if tc.retry {
					require.False(t, login(), "retry must succeed")
				}

				if tc.expectNoRow {
					_, err = wrapped.GetUpstreamTokens(userCtx, newSessionID, providerName)
					assert.ErrorIs(t, err, storage.ErrNotFound)
					return
				}

				// With the field on, the row lives under the user's key and is read
				// back through the wrapper; off, it is under the login's session.
				var stored *storage.UpstreamTokens
				if tc.userKeyed {
					stored, err = wrapped.GetUpstreamTokens(userCtx, newSessionID, providerName)
					require.NoError(t, err)
					assert.NotContains(t, storState.upstreamTokens, newSessionID+":"+providerName,
						"user-keyed rows must not be keyed by the login's session")
				} else {
					stored = storState.upstreamTokens[newSessionID+":"+providerName]
					require.NotNil(t, stored)
				}
				assert.Equal(t, tc.expectedRT, stored.RefreshToken)
				assert.Equal(t, tc.expectedAT, stored.AccessToken)
				if tc.expectedSub != "" {
					assert.Equal(t, tc.expectedSub, stored.UpstreamSubject)
				}
				if tc.retry {
					assert.Equal(t, idpSubject, stored.UpstreamSubject)
				}
			})
		}
	}
}
