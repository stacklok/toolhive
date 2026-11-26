// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/oauth2-proxy/mockoidc"
	"github.com/ory/fosite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/auth/upstreamtoken"
	"github.com/stacklok/toolhive/pkg/authserver/claimnames"
	serveridentity "github.com/stacklok/toolhive/pkg/authserver/identity"
	servercrypto "github.com/stacklok/toolhive/pkg/authserver/server/crypto"
	"github.com/stacklok/toolhive/pkg/authserver/server/handlers"
	"github.com/stacklok/toolhive/pkg/authserver/server/session"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/authserver/storage/mocks"
	"github.com/stacklok/toolhive/pkg/authserver/upstream"
	upstreammocks "github.com/stacklok/toolhive/pkg/authserver/upstream/mocks"
)

// The user-keyed guard logs through the global slog, so the tests that count
// its WARN lines install a capturing default logger. They are not parallel: a
// parallel test would log into the capture or race the swap.

type warnRecorder struct {
	mu   sync.Mutex
	msgs []string
}

func (*warnRecorder) Enabled(context.Context, slog.Level) bool { return true }

func (h *warnRecorder) Handle(_ context.Context, r slog.Record) error {
	if r.Level >= slog.LevelWarn {
		h.mu.Lock()
		h.msgs = append(h.msgs, r.Message)
		h.mu.Unlock()
	}
	return nil
}

func (h *warnRecorder) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *warnRecorder) WithGroup(string) slog.Handler      { return h }

func (h *warnRecorder) reset() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.msgs = nil
}

func (h *warnRecorder) count(msg string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, m := range h.msgs {
		if m == msg {
			n++
		}
	}
	return n
}

func (h *warnRecorder) total() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.msgs)
}

func captureWarns(t *testing.T) *warnRecorder {
	t.Helper()
	rec := &warnRecorder{}
	previous := slog.Default()
	slog.SetDefault(slog.New(rec))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return rec
}

// foreignFromReadStorage sits below the wrapper and answers every read from
// the foreignFrom-th on with a row owned by someone else, as a row replaced
// between the guard's read and the refresher's re-read would look. With
// failSaves set, every compare-and-swap fails with a storage error, as when the
// refresher cannot save a refreshed row.
type foreignFromReadStorage struct {
	storage.Storage
	mu          sync.Mutex
	reads       int
	foreignFrom int
	failSaves   bool
	// afterRead runs once, right after the next read, and the read still
	// returns what it read before the hook ran.
	afterRead func()
}

func (s *foreignFromReadStorage) runAfterNextRead(f func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.afterRead = f
}

func (s *foreignFromReadStorage) CompareAndSwapUpstreamTokens(
	ctx context.Context, sessionID, provider, expectedRefreshToken string, tokens *storage.UpstreamTokens,
) error {
	if s.failSaves {
		return errors.New("storage unavailable")
	}
	return s.Storage.CompareAndSwapUpstreamTokens(ctx, sessionID, provider, expectedRefreshToken, tokens)
}

func (s *foreignFromReadStorage) GetUpstreamTokens(
	ctx context.Context, sessionID, provider string,
) (*storage.UpstreamTokens, error) {
	row, err := s.Storage.GetUpstreamTokens(ctx, sessionID, provider)
	s.mu.Lock()
	s.reads++
	foreign := s.foreignFrom > 0 && s.reads >= s.foreignFrom
	hook := s.afterRead
	s.afterRead = nil
	s.mu.Unlock()
	if hook != nil {
		defer hook()
	}

	if foreign && row != nil {
		copied := *row
		copied.UserID = "someone-else"
		return &copied, err
	}
	return row, err
}

const guardUserID = "user" // guardTestRow's UserID and guardTestRequest's subject

func guardUserCtx() context.Context {
	return serveridentity.WithCanonicalUser(context.Background(), guardUserID)
}

func guardRequestFor(subject, tsid string) *fosite.AccessRequest {
	req := fosite.NewAccessRequest(session.New(subject, tsid, testClientID, session.UserClaims{}))
	req.GrantTypes = []string{"refresh_token"}
	req.Client = &fosite.DefaultClient{ID: testClientID}
	req.SetID(guardTestRequestID)
	return req
}

type userKeyedGuardEnv struct {
	base    *storage.MemoryStorage
	reads   *foreignFromReadStorage
	wrapper *storage.UserKeyedUpstreamStorage
	spy     *deleteSpyStorage
	guard   *upstreamRefreshGuard
	idp     string
	conn    string
	// release unblocks a hung IdP refresh.
	release chan struct{}
}

// rowKey is the key the wrapper uses in the store below it.
func (*userKeyedGuardEnv) rowKey() string {
	return userKeyedRowSessionPrefix + url.QueryEscape(guardUserID)
}

func (e *userKeyedGuardEnv) put(t *testing.T, provider string, row *storage.UpstreamTokens) {
	t.Helper()
	require.NoError(t, e.store(provider, row))
}

// store is put for callers off the test goroutine.
func (e *userKeyedGuardEnv) store(provider string, row *storage.UpstreamTokens) error {
	stored := *row
	stored.ProviderID = provider
	return e.wrapper.StoreUpstreamTokens(guardUserCtx(), "ignored", provider, &stored)
}

// userKeyedGuardCase is one row of the user-keyed guard table. The real
// refresher runs over the real wrapper; only the providers are mocks, so the
// error chain the guard classifies is the one production produces.
type userKeyedGuardCase struct {
	name            string
	idpRow, connRow *storage.UpstreamTokens
	// foreignIdP plants a row owned by someone else under the user's key.
	foreignIdP bool
	// foreignFromRead makes the nth read below the wrapper return a foreign row.
	foreignFromRead int
	// failSaves makes every save of a refreshed row fail below the wrapper.
	failSaves bool
	// idpRefresh, when set, is the one expected refresh of the identity
	// provider; the connector's provider allows no call at all.
	idpRefresh func(t *testing.T, env *userKeyedGuardEnv, refreshToken string) (*upstream.Tokens, error)
	// winnerAfter is stored as the row after the refresher's re-read that
	// follows a rejection and before the guard's recheck.
	winnerAfter  *storage.UpstreamTokens
	cancelled    bool
	hang         bool
	emptySubject bool
	unconfigured bool
	recheckDelay time.Duration
	wantRefuse   bool
	wantRevoke   bool
	// wantWarn counts the guard's WARN line. When it is set, it must also be
	// the only WARN logged, so the failure must come from a 5xx or a timeout:
	// a failed save or a refresh conflict makes the refresher log its own line.
	wantWarn int
	// wantStoredRT, when set, is the refresh token the identity provider row
	// must hold at the end; "" means the row is kept with its token cleared.
	wantStoredRT *string
	// refresherDeletes is set when the refresher itself removes the row (a
	// rotation it could not persist), which is not the guard deleting.
	refresherDeletes bool
}

//nolint:paralleltest // captureWarns replaces the process-wide default logger.
func TestUserKeyedUpstreamRefreshGuard(t *testing.T) {
	warns := captureWarns(t)

	past := time.Now().Add(-time.Minute)
	future := time.Now().Add(time.Hour)
	rejected := fmt.Errorf("upstream rejected: %w", upstream.ErrGrantRejected)
	refreshed := func() *upstream.Tokens {
		return &upstream.Tokens{AccessToken: "fresh", RefreshToken: "rt", ExpiresAt: future}
	}
	rotatedAfter := func(*testing.T, *userKeyedGuardEnv, string) (*upstream.Tokens, error) {
		return nil, rejected
	}

	expiredIDToken, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"exp": time.Now().Add(-time.Hour).Unix(),
	}).SignedString([]byte("test-key"))
	require.NoError(t, err)
	liveWithExpiredIDToken := guardTestRow(future, "rt")
	liveWithExpiredIDToken.IDToken = expiredIDToken

	tests := []userKeyedGuardCase{
		{name: "no identity provider row is refused and revoked", wantRefuse: true, wantRevoke: true},
		{name: "foreign identity provider row is refused without revoking", foreignIdP: true, wantRefuse: true},
		{
			name:   "row replaced by a foreign one before the refresher's re-read is refused without revoking",
			idpRow: guardTestRow(past, "rt"),
			// Reads below the wrapper: 1 is the guard's snapshot, 2 the reader's,
			// 3 the refresher's re-read.
			foreignFromRead: 3,
			wantRefuse:      true,
		},
		{
			name:       "expired row without a refresh token is refused and revoked",
			idpRow:     guardTestRow(past, ""),
			wantRefuse: true, wantRevoke: true,
		},
		{
			name:   "invalid_grant is refused and revoked, and the row is kept",
			idpRow: guardTestRow(past, "rt"),
			idpRefresh: func(*testing.T, *userKeyedGuardEnv, string) (*upstream.Tokens, error) {
				return nil, rejected
			},
			wantRefuse: true, wantRevoke: true, wantStoredRT: ptr(""),
		},
		{
			name:   "row deleted inside the refresh is refused and revoked",
			idpRow: guardTestRow(past, "rt"),
			idpRefresh: func(t *testing.T, env *userKeyedGuardEnv, _ string) (*upstream.Tokens, error) {
				t.Helper()
				assert.NoError(t, env.base.DeleteUpstreamTokensForProvider(context.Background(), env.rowKey(), env.idp))
				return &upstream.Tokens{AccessToken: "fresh", RefreshToken: "rotated-rt", ExpiresAt: future}, nil
			},
			wantRefuse: true, wantRevoke: true, refresherDeletes: true,
		},
		{
			// The refresher deletes a row whose rotated refresh token it could
			// not save; its error wraps storage.ErrNotFound, so the reader
			// reports that the user needs to authorize again.
			name:   "row the refresher deleted after failing to save a rotated refresh token is refused and revoked",
			idpRow: guardTestRow(past, "rt"),
			idpRefresh: func(*testing.T, *userKeyedGuardEnv, string) (*upstream.Tokens, error) {
				return &upstream.Tokens{AccessToken: "fresh", RefreshToken: "rotated-rt", ExpiresAt: future}, nil
			},
			failSaves:  true,
			wantRefuse: true, wantRevoke: true, refresherDeletes: true,
		},
		{
			name:   "refresh that leaves no access token is refused and revoked",
			idpRow: guardTestRow(past, "rt"),
			idpRefresh: func(*testing.T, *userKeyedGuardEnv, string) (*upstream.Tokens, error) {
				return &upstream.Tokens{RefreshToken: "rotated-rt", ExpiresAt: future}, nil
			},
			wantRefuse: true, wantRevoke: true,
		},
		{name: "session without a subject is refused without revoking", idpRow: guardTestRow(future, "rt"), emptySubject: true, wantRefuse: true},
		{name: "identity provider that is not configured is refused without revoking", idpRow: guardTestRow(future, "rt"), unconfigured: true, wantRefuse: true},
		{
			name: "live row without an access token counts as absent and is refused and revoked",
			idpRow: func() *storage.UpstreamTokens {
				row := guardTestRow(future, "rt")
				row.AccessToken = ""
				return row
			}(),
			wantRefuse: true, wantRevoke: true,
		},
		{
			name:   "live access token with an expired ID token is granted without calling the identity provider",
			idpRow: liveWithExpiredIDToken,
		},
		{name: "live identity provider row with no refresh token is granted", idpRow: guardTestRow(future, "")},
		{name: "non-expiring identity provider row is granted", idpRow: guardTestRow(time.Time{}, "")},
		{
			name:   "idle expired row whose refresh succeeds is granted",
			idpRow: guardTestRow(past, "rt"),
			idpRefresh: func(*testing.T, *userKeyedGuardEnv, string) (*upstream.Tokens, error) {
				return refreshed(), nil
			},
		},
		{
			name:   "5xx is granted with one WARN",
			idpRow: guardTestRow(past, "rt"),
			idpRefresh: func(*testing.T, *userKeyedGuardEnv, string) (*upstream.Tokens, error) {
				return nil, errors.New("upstream token refresh failed: 503 Service Unavailable")
			},
			wantWarn: 1,
		},
		{
			name:       "invalid_grant after another replica rotated the row is granted",
			idpRow:     guardTestRow(past, "rt"),
			idpRefresh: rotatedAfter,
			winnerAfter: func() *storage.UpstreamTokens {
				return guardTestRow(future, "rotated-rt")
			}(),
			wantStoredRT: ptr("rotated-rt"),
		},
		{name: "cancelled request is granted without a WARN", idpRow: guardTestRow(past, "rt"), cancelled: true},
		{
			name:     "hung identity provider is cut off by the check timeout and granted with one WARN",
			idpRow:   guardTestRow(past, "rt"),
			hang:     true,
			wantWarn: 1,
			idpRefresh: func(*testing.T, *userKeyedGuardEnv, string) (*upstream.Tokens, error) {
				return refreshed(), nil
			},
		},
		{
			// The connector row is dead but refreshable; its provider is a
			// strict mock, so the row also proves the connector is never asked.
			name:   "identity provider named after the connector is the one checked",
			idpRow: guardTestRow(future, "rt"), connRow: guardTestRow(past, "rt-conn"),
		},
		{
			name:    "a live connector does not save an absent identity provider that sorts last, which is revoked",
			connRow: guardTestRow(future, "rt"), wantRefuse: true, wantRevoke: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			warns.reset()
			runUserKeyedGuardCase(t, warns, tt)
		})
	}
}

func runUserKeyedGuardCase(t *testing.T, warns *warnRecorder, tt userKeyedGuardCase) {
	t.Helper()
	ctx := context.Background()
	// The identity provider is the first upstream even though its name sorts
	// after the connector's.
	env := &userKeyedGuardEnv{idp: "zeta-idp", conn: "alpha-conn", release: make(chan struct{})}

	env.base = storage.NewMemoryStorage()
	t.Cleanup(func() { _ = env.base.Close() })
	env.reads = &foreignFromReadStorage{Storage: env.base, foreignFrom: tt.foreignFromRead, failSaves: tt.failSaves}
	wrapper, err := storage.NewUserKeyedUpstreamStorage(env.reads)
	require.NoError(t, err)
	env.wrapper = wrapper
	env.spy = &deleteSpyStorage{Storage: env.wrapper}

	ctrl := gomock.NewController(t)
	// Registered after the controller, so it runs first: a hung refresh is
	// released and has returned before the controller checks its calls and the
	// storage closes.
	refreshExited := make(chan struct{})
	t.Cleanup(func() {
		close(env.release)
		if !tt.hang {
			return
		}
		select {
		case <-refreshExited:
		case <-time.After(5 * time.Second):
			t.Error("the hung identity provider refresh did not return after its release")
		}
	})

	idpProvider := upstreammocks.NewMockOAuth2Provider(ctrl)
	connProvider := upstreammocks.NewMockOAuth2Provider(ctrl) // no expectations: any call fails the test
	if tt.idpRefresh != nil {
		idpProvider.EXPECT().RefreshTokens(gomock.Any(), gomock.Any(), gomock.Any()).Times(1).DoAndReturn(
			func(_ context.Context, refreshToken, _ string) (*upstream.Tokens, error) {
				if tt.hang {
					defer close(refreshExited)
					<-env.release
				}
				if tt.winnerAfter != nil {
					winner := *tt.winnerAfter
					env.reads.runAfterNextRead(func() { assert.NoError(t, env.store(env.idp, &winner)) })
				}
				return tt.idpRefresh(t, env, refreshToken)
			})
	}

	if tt.idpRow != nil {
		env.put(t, env.idp, tt.idpRow)
	}
	if tt.connRow != nil {
		env.put(t, env.conn, tt.connRow)
	}
	if tt.foreignIdP {
		foreign := guardTestRow(time.Now().Add(time.Hour), "rt")
		foreign.UserID, foreign.ProviderID = "someone-else", env.idp
		require.NoError(t, env.base.StoreUpstreamTokens(ctx, env.rowKey(), env.idp, foreign))
	}

	names := []string{env.idp, env.conn}
	refresher := newUpstreamTokenRefresher([]handlers.NamedUpstream{
		{Name: env.idp, Provider: idpProvider}, {Name: env.conn, Provider: connProvider},
	}, env.spy, time.Hour, true)
	guard, err := newUserKeyedUpstreamRefreshGuard(env.spy, refresher, names)
	require.NoError(t, err)
	guard.recheckDelay = tt.recheckDelay
	if tt.hang {
		guard.checkTimeout = 50 * time.Millisecond
	}
	if tt.unconfigured {
		delete(guard.providers, env.idp)
	}
	env.guard = guard

	subject := guardUserID
	if tt.emptySubject {
		subject = ""
	}
	req := guardRequestFor(subject, guardTestTSID)
	require.NoError(t, env.base.CreateRefreshTokenSession(ctx, "rt-sig", "at-sig", req))
	require.NoError(t, env.base.CreateAccessTokenSession(ctx, "at-sig", req))

	// The result must arrive while a hung refresh is still blocked.
	reqCtx, cancelReq := context.WithCancel(ctx)
	t.Cleanup(cancelReq)
	if tt.cancelled {
		cancelReq()
	}
	done := make(chan error, 1)
	go func() { done <- guard.HandleTokenEndpointRequest(reqCtx, req) }()
	var handleErr error
	select {
	case handleErr = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the guard did not return while the identity provider was blocked")
	}

	if tt.wantRefuse {
		require.ErrorIs(t, handleErr, fosite.ErrInvalidGrant)
	} else {
		require.NoError(t, handleErr)
	}

	_, refreshErr := env.base.GetRefreshTokenSession(ctx, "rt-sig", nil)
	_, accessErr := env.base.GetAccessTokenSession(ctx, "at-sig", nil)
	if tt.wantRevoke {
		assert.Error(t, refreshErr, "the refused refresh token must be revoked")
		assert.Error(t, accessErr, "the refused grant's access token must be revoked")
	} else {
		assert.NoError(t, refreshErr, "the refresh token must not be revoked")
		assert.NoError(t, accessErr, "the access token must not be revoked")
	}

	assert.Equal(t, tt.wantWarn, warns.count(guardCheckFailedMessage))
	if tt.wantWarn > 0 {
		assert.Equal(t, tt.wantWarn, warns.total(), "the guard's WARN must be the only one")
	}
	if tt.wantStoredRT != nil {
		row, err := env.base.GetUpstreamTokens(ctx, env.rowKey(), env.idp)
		require.True(t, err == nil || errors.Is(err, storage.ErrExpired), "the identity provider row must still be stored")
		require.NotNil(t, row)
		assert.Equal(t, *tt.wantStoredRT, row.RefreshToken)
	}
	if tt.refresherDeletes {
		_, err := env.base.GetUpstreamTokens(ctx, env.rowKey(), env.idp)
		assert.ErrorIs(t, err, storage.ErrNotFound, "the refresher must have removed the row")
	} else {
		assert.Zero(t, env.spy.deletes.Load(), "the guard must delete no upstream row")
		if tt.idpRow != nil {
			_, err := env.base.GetUpstreamTokens(ctx, env.rowKey(), env.idp)
			assert.False(t, errors.Is(err, storage.ErrNotFound), "the identity provider row must still be stored")
		}
		if tt.connRow != nil {
			_, err := env.base.GetUpstreamTokens(ctx, env.rowKey(), env.conn)
			assert.False(t, errors.Is(err, storage.ErrNotFound), "the connector row must still be stored")
		}
	}
}

// TestUserKeyedUpstreamRefreshGuard_Storage pins the fail-open storage paths
// and the calls the guard makes, against mock storage.
//
//nolint:paralleltest // captureWarns replaces the process-wide default logger.
func TestUserKeyedUpstreamRefreshGuard_Storage(t *testing.T) {
	warns := captureWarns(t)
	storageErr := errors.New("storage unavailable")

	tests := []struct {
		name       string
		tsid       string
		setup      func(stor *mocks.MockStorage)
		wantRefuse bool
		// wantRefreshCall expects the one upstream refresh whose rejection is confirmed.
		wantRefreshCall bool
		wantWarn        int
	}{
		{
			// Not revoked (the mock storage has no revoke expectation).
			name: "no canonical user for the row is refused without revoking",
			tsid: guardTestTSID,
			setup: func(stor *mocks.MockStorage) {
				stor.EXPECT().GetUpstreamTokens(gomock.Any(), gomock.Any(), "idp").
					Return(nil, storage.ErrNoCanonicalUser).AnyTimes()
			},
			wantRefuse: true,
		},
		{name: "session without a tsid reads nothing", tsid: "", setup: func(*mocks.MockStorage) {}},
		{
			name: "storage failure grants with one WARN",
			tsid: guardTestTSID,
			setup: func(stor *mocks.MockStorage) {
				stor.EXPECT().GetUpstreamTokens(gomock.Any(), gomock.Any(), "idp").Return(nil, storageErr).AnyTimes()
			},
			wantWarn: 1,
		},
		{
			name: "invalid_grant with an unreadable snapshot is granted even if the recheck reads an empty token",
			tsid: guardTestTSID,
			setup: func(stor *mocks.MockStorage) {
				reads := 0
				stor.EXPECT().GetUpstreamTokens(gomock.Any(), gomock.Any(), "idp").DoAndReturn(
					func(context.Context, string, string) (*storage.UpstreamTokens, error) {
						reads++
						switch reads {
						case 1:
							return nil, storageErr
						case 2:
							return guardTestRow(time.Now().Add(-time.Minute), "rt"), storage.ErrExpired
						default:
							return guardTestRow(time.Now().Add(-time.Minute), ""), storage.ErrExpired
						}
					}).AnyTimes()
			},
			wantRefreshCall: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			warns.reset()
			ctrl := gomock.NewController(t)
			stor := mocks.NewMockStorage(ctrl) // no Delete expectations: any delete fails the test
			refresher := mocks.NewMockUpstreamTokenRefresher(ctrl)
			tt.setup(stor)
			if tt.wantRefreshCall {
				refresher.EXPECT().RefreshAndStore(gomock.Any(), gomock.Any(), gomock.Any()).
					Return(nil, fmt.Errorf("refresh failed: %w", upstream.ErrGrantRejected))
			}
			guard, err := newUserKeyedUpstreamRefreshGuard(stor, refresher, []string{"idp"})
			require.NoError(t, err)
			guard.recheckDelay = 0

			err = guard.HandleTokenEndpointRequest(context.Background(), guardRequestFor(guardUserID, tt.tsid))
			if tt.wantRefuse {
				require.ErrorIs(t, err, fosite.ErrInvalidGrant)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tt.wantWarn, warns.count(guardCheckFailedMessage))
			if tt.wantWarn > 0 {
				assert.Equal(t, tt.wantWarn, warns.total(), "the guard's WARN must be the only one")
			}
		})
	}
}

func TestNewUserKeyedUpstreamRefreshGuard_RequiresAnIdentityProvider(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	_, err := newUserKeyedUpstreamRefreshGuard(mocks.NewMockStorage(ctrl), mocks.NewMockUpstreamTokenRefresher(ctrl), nil)
	require.Error(t, err)
}

// TestIntegration_UpstreamRefreshGuard_UserKeyed drives the token endpoint end
// to end with user-keyed storage, on memory and Redis: a refused grant never
// deletes an upstream row, and a refresh token refused on a confirmed
// rejection stays dead after the user signs in again on another client.
func TestIntegration_UpstreamRefreshGuard_UserKeyed(t *testing.T) {
	t.Parallel()

	type env struct {
		ts   *testServerWithUpstream
		sub  string
		tsid string
		spy  *deleteSpyStorage
	}
	userCtx := func(e *env) context.Context { return canonicalUserCtx(e.sub) }
	row := func(t *testing.T, e *env) *storage.UpstreamTokens {
		t.Helper()
		got, err := e.ts.storage.GetUpstreamTokens(userCtx(e), e.tsid, "default")
		if err != nil {
			require.ErrorIs(t, err, storage.ErrExpired)
		}
		require.NotNil(t, got)
		copied := *got
		return &copied
	}
	put := func(t *testing.T, e *env, r *storage.UpstreamTokens) {
		t.Helper()
		require.NoError(t, e.ts.storage.StoreUpstreamTokens(userCtx(e), e.tsid, "default", r))
	}
	rejectRefresh := func(t *testing.T, e *env) {
		t.Helper()
		unknown, err := e.ts.mockOIDC.Keypair.SignJWT(jwt.MapClaims{
			"jti": "no-such-session", "exp": time.Now().Add(time.Hour).Unix(),
		})
		require.NoError(t, err)
		r := row(t, e)
		r.ExpiresAt, r.RefreshToken = time.Now().Add(-time.Minute), unknown
		put(t, e, r)
	}

	tests := []struct {
		name       string
		mutate     func(t *testing.T, e *env)
		redisOnly  bool
		wantRefuse bool
		wantRevoke bool
		// wantRowKept asserts the identity provider's row is still stored.
		wantRowKept bool
		wantRefresh bool
	}{
		{name: "live row is granted", mutate: func(*testing.T, *env) {}, wantRowKept: true},
		{
			name: "idle row whose refresh succeeds is granted",
			mutate: func(t *testing.T, e *env) {
				t.Helper()
				r := row(t, e)
				r.ExpiresAt = time.Now().Add(-time.Minute)
				put(t, e, r)
			},
			wantRowKept: true, wantRefresh: true,
		},
		{
			name: "absent row is refused and revoked, and a later login on another client does not revive it",
			mutate: func(t *testing.T, e *env) {
				t.Helper()
				require.NoError(t, e.ts.storage.DeleteUpstreamTokensForProvider(userCtx(e), e.tsid, "default"))
			},
			wantRefuse: true, wantRevoke: true,
		},
		{
			name: "foreign row is refused without revoking",
			mutate: func(t *testing.T, e *env) {
				t.Helper()
				base := storage.Unwrap(serverOf(t, e.ts.testServer).storage)
				require.NoError(t, base.StoreUpstreamTokens(context.Background(),
					userKeyedRowSessionPrefix+url.QueryEscape(e.sub), "default", &storage.UpstreamTokens{
						ProviderID: "default", AccessToken: "intruder", UserID: "someone-else",
						ExpiresAt: time.Now().Add(time.Hour), UpstreamSubject: "x", ClientID: testClientID,
					}))
			},
			wantRefuse: true,
		},
		{name: "invalid_grant is refused and revoked, and the row is kept", mutate: rejectRefresh,
			wantRefuse: true, wantRevoke: true, wantRowKept: true},
		{
			name: "expired row without a refresh token is refused and revoked",
			mutate: func(t *testing.T, e *env) {
				t.Helper()
				r := row(t, e)
				r.ExpiresAt, r.RefreshToken = time.Now().Add(-time.Minute), ""
				put(t, e, r)
			},
			wantRefuse: true, wantRevoke: true, wantRowKept: true,
		},
		{
			name: "unreadable row on Redis is granted as an uncertain read",
			mutate: func(t *testing.T, e *env) {
				t.Helper()
				key := "test:auth:{ns:test}:" + storage.KeyTypeUpstream + ":user:" + url.QueryEscape(e.sub) + ":default"
				mr := e.ts.Miniredis(t)
				require.True(t, mr.Exists(key), "upstream row key must exist")
				require.NoError(t, mr.Set(key, "{not json"))
			},
			redisOnly: true,
		},
	}

	for _, backend := range userKeyedBackends {
		for _, tt := range tests {
			if tt.redisOnly && backend.name != "redis" {
				continue
			}
			t.Run(backend.name+"/"+tt.name, func(t *testing.T) {
				t.Parallel()
				spy := &deleteSpyStorage{}
				embedder := &recordingRefreshHandler{}
				ts := setupTestServerWithMockOIDC(t, startMockOIDC(t), userKeyedOpts(backend.opts,
					withExtraClient(userKeyedSecondClient()),
					withRefreshGrantHandlers(embedder),
					withStorageDecorator(func(s storage.Storage) storage.Storage {
						spy.Storage = s
						return spy
					}))...)

				login := func(clientID string) map[string]any {
					verifier := servercrypto.GeneratePKCEVerifier()
					code, _ := completeAuthorizationFlow(t, ts.Server.URL, authorizationParams{
						ClientID: clientID, RedirectURI: testRedirectURI, State: "guard-user-keyed",
						Challenge: servercrypto.ComputePKCEChallenge(verifier),
						Scope:     "openid profile offline_access", ResponseType: "code",
					})
					return exchangeCodeForTokensAs(t, ts.Server.URL, clientID, code, verifier, testAudience)
				}
				tokens := login(testClientID)
				claims := claimsFromTokens(t, ts.testServer, tokens)
				e := &env{ts: ts, sub: stringClaim(t, claims, "sub"), tsid: stringClaim(t, claims, claimnames.TokenSessionIDKey), spy: spy}
				refreshToken, ok := tokens["refresh_token"].(string)
				require.True(t, ok)

				tt.mutate(t, e)
				spy.deletes.Store(0)

				refresh := func() (int, map[string]any) {
					resp := makeTokenRequest(t, ts.Server.URL, url.Values{
						"grant_type": {"refresh_token"}, "refresh_token": {refreshToken}, "client_id": {testClientID},
					})
					defer resp.Body.Close()
					return resp.StatusCode, parseTokenResponse(t, resp)
				}

				status, body := refresh()
				assert.Zero(t, spy.deletes.Load(), "the guard must delete no upstream row")
				if !tt.wantRefuse {
					require.Equal(t, http.StatusOK, status, "refresh grant must be granted: %v", body)
					assert.Len(t, embedder.handledGrants(), 1)
					if tt.wantRefresh {
						assert.False(t, row(t, e).IsExpired(time.Now()), "the guard must refresh the expired row")
					}
					return
				}
				assert.Equal(t, http.StatusBadRequest, status)
				assert.Equal(t, "invalid_grant", body["error"])
				assert.NotContains(t, body, "access_token")
				assert.Empty(t, embedder.handledGrants(), "embedder handlers must not run after the guard refuses")
				if tt.wantRowKept {
					base := storage.Unwrap(serverOf(t, ts.testServer).storage)
					_, err := base.GetUpstreamTokens(context.Background(), userKeyedRowSessionPrefix+url.QueryEscape(e.sub), "default")
					assert.False(t, errors.Is(err, storage.ErrNotFound), "the row must still be stored")
				}

				// The user signs in again on another client, which stores a live row.
				ts.mockOIDC.QueueUser(&mockoidc.MockUser{Subject: "mock-user-sub-123", Email: "testuser@example.com"})
				second := login(userKeyedSecondClientID)
				require.Equal(t, e.sub, stringClaim(t, claimsFromTokens(t, ts.testServer, second), "sub"))

				status, body = refresh()
				if tt.wantRevoke {
					assert.NotEqual(t, http.StatusOK, status, "a refresh token refused on dead credentials must stay revoked: %v", body)
					assert.NotContains(t, body, "access_token")
					return
				}
				assert.Equal(t, http.StatusOK, status, "a refresh token refused without revocation works again: %v", body)
			})
		}
	}
}

// userKeyedRememberedEnv is a user-keyed guard setup over the real refresher
// and an IdP that rejects the user's refresh token, with that rejection
// already remembered from a bearer request.
type userKeyedRememberedEnv struct {
	base      *storage.MemoryStorage
	wrapper   *storage.UserKeyedUpstreamStorage
	idp       *countingIdP
	refresher *upstreamTokenRefresher
	req       *fosite.AccessRequest
}

func newUserKeyedRememberedEnv(t *testing.T) *userKeyedRememberedEnv {
	t.Helper()
	ctx := context.Background()
	env := &userKeyedRememberedEnv{idp: newCountingIdP(t, http.StatusBadRequest, invalidGrantBody)}
	env.base = storage.NewMemoryStorage()
	t.Cleanup(func() { _ = env.base.Close() })
	wrapper, err := storage.NewUserKeyedUpstreamStorage(env.base)
	require.NoError(t, err)
	env.wrapper = wrapper
	env.refresher = newUpstreamTokenRefresher(
		[]handlers.NamedUpstream{{Name: "idp", Provider: env.idp.provider(t)}}, wrapper, time.Hour, true)
	require.NoError(t, wrapper.StoreUpstreamTokens(
		guardUserCtx(), "ignored", "idp", guardTestRow(time.Now().Add(-time.Minute), "dead-rt")))

	reader := upstreamtoken.NewInProcessService(wrapper, env.refresher)
	_, err = reader.GetValidTokensForUser(ctx, guardUserID, "idp")
	require.ErrorIs(t, err, upstream.ErrGrantRejected)
	_, err = reader.GetValidTokensForUser(ctx, guardUserID, "idp")
	require.True(t, isRememberedRejection(err), "the bearer path must answer from the memory")

	env.req = guardRequestFor(guardUserID, guardTestTSID)
	require.NoError(t, env.base.CreateRefreshTokenSession(ctx, "rt-sig", "at-sig", env.req))
	require.NoError(t, env.base.CreateAccessTokenSession(ctx, "at-sig", env.req))
	return env
}

func (e *userKeyedRememberedEnv) guard(t *testing.T, refresher storage.UpstreamTokenRefresher) *upstreamRefreshGuard {
	t.Helper()
	guard, err := newUserKeyedUpstreamRefreshGuard(e.wrapper, refresher, []string{"idp"})
	require.NoError(t, err)
	guard.recheckDelay = 0
	return guard
}

func (e *userKeyedRememberedEnv) revoked(t *testing.T) bool {
	t.Helper()
	_, err := e.base.GetRefreshTokenSession(context.Background(), "rt-sig", nil)
	return err != nil
}

// TestUserKeyedUpstreamRefreshGuard_RememberedRejection covers a guard whose
// IdP rejection was already remembered from a bearer request.
func TestUserKeyedUpstreamRefreshGuard_RememberedRejection(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		idpStatus int
		idpBody   string
		// rememberedCalls is how many of the guard's two refresher calls
		// answer from the memory, counting from the first. Zero means the
		// guard talks to the real refresher directly.
		rememberedCalls int
		wantIdPCalls    int
		wantRefuse      bool
	}{
		{
			name:      "asks the IdP despite the remembered rejection and it still rejects",
			idpStatus: http.StatusBadRequest, idpBody: invalidGrantBody,
			wantIdPCalls: 1, wantRefuse: true,
		},
		{
			// An administrator cleared an IdP-side block after the rejection
			// was remembered.
			name:      "asks the IdP despite the remembered rejection and it now accepts",
			idpStatus: http.StatusOK, idpBody: refreshOKBody,
			wantIdPCalls: 1,
		},
		{
			// The first call stands in for joining a refresh led by a request
			// that does not skip the memory.
			name:      "retries a joined remembered rejection and the IdP still rejects",
			idpStatus: http.StatusBadRequest, idpBody: invalidGrantBody,
			rememberedCalls: 1, wantIdPCalls: 1, wantRefuse: true,
		},
		{
			name:      "retries a joined remembered rejection and the IdP now accepts",
			idpStatus: http.StatusOK, idpBody: refreshOKBody,
			rememberedCalls: 1, wantIdPCalls: 1,
		},
		{
			name:      "grants when every call answers with a remembered rejection",
			idpStatus: http.StatusBadRequest, idpBody: invalidGrantBody,
			rememberedCalls: 2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			env := newUserKeyedRememberedEnv(t)
			env.idp.set(tt.idpStatus, tt.idpBody)
			callsBefore := env.idp.calls.Load()

			var refresher storage.UpstreamTokenRefresher = env.refresher
			var calls atomic.Int32
			if tt.rememberedCalls > 0 {
				mock := mocks.NewMockUpstreamTokenRefresher(gomock.NewController(t))
				mock.EXPECT().RefreshAndStore(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
					func(ctx context.Context, sessionID string, expired *storage.UpstreamTokens) (*storage.UpstreamTokens, error) {
						// assert, not require: this runs on the guard's goroutine.
						assert.True(t, skipsRememberedRejections(ctx), "the guard must mark its context")
						if int(calls.Add(1)) <= tt.rememberedCalls {
							tokens, err := env.refresher.RefreshAndStore(guardUserCtx(), sessionID, expired)
							assert.True(t, isRememberedRejection(err))
							return tokens, err
						}
						return env.refresher.RefreshAndStore(ctx, sessionID, expired)
					}).Times(2)
				refresher = mock
			}

			err := env.guard(t, refresher).HandleTokenEndpointRequest(context.Background(), env.req)

			if tt.rememberedCalls > 0 {
				assert.Equal(t, int32(2), calls.Load())
			}
			assert.Equal(t, callsBefore+int32(tt.wantIdPCalls), env.idp.calls.Load(), "calls that reach the IdP")
			if tt.wantRefuse {
				require.ErrorIs(t, err, fosite.ErrInvalidGrant)
				assert.True(t, env.revoked(t), "a rejection the IdP just gave revokes the refresh token")
			} else {
				require.NoError(t, err)
				assert.False(t, env.revoked(t), "the refresh token must not be revoked")
			}
			got, err := env.wrapper.GetUpstreamTokens(guardUserCtx(), "ignored", "idp")
			if tt.idpStatus == http.StatusOK {
				require.NoError(t, err)
				assert.Equal(t, "new-access", got.AccessToken)
			}
			_, err = env.base.GetUpstreamTokens(context.Background(), userKeyedRowSessionPrefix+url.QueryEscape(guardUserID), "idp")
			assert.NotErrorIs(t, err, storage.ErrNotFound, "the guard must delete no upstream row")
		})
	}
}

func TestUserKeyedUpstreamRefreshGuard_ClearRejectedRefreshToken(t *testing.T) {
	t.Parallel()
	future := time.Now().Add(time.Hour)

	tests := []struct {
		name string
		// stored is the refresh token on the row when the guard clears.
		stored string
		wantRT string
	}{
		{name: "the rejected token is cleared and the row is kept", stored: "rejected-rt", wantRT: ""},
		{name: "a token stored by a newer sign-in is not cleared", stored: "newer-rt", wantRT: "newer-rt"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			base := storage.NewMemoryStorage()
			t.Cleanup(func() { _ = base.Close() })
			wrapper, err := storage.NewUserKeyedUpstreamStorage(base)
			require.NoError(t, err)
			ctrl := gomock.NewController(t)
			guard, err := newUserKeyedUpstreamRefreshGuard(wrapper, mocks.NewMockUpstreamTokenRefresher(ctrl), []string{"idp"})
			require.NoError(t, err)
			require.NoError(t, wrapper.StoreUpstreamTokens(guardUserCtx(), "ignored", "idp", guardTestRow(future, tt.stored)))

			guard.clearRejectedRefreshToken(guardUserCtx(), guardTestTSID, "idp", guardTestRow(future, "rejected-rt"))

			row, err := wrapper.GetUpstreamTokens(guardUserCtx(), guardTestTSID, "idp")
			require.NoError(t, err)
			assert.Equal(t, tt.wantRT, row.RefreshToken)
		})
	}
}

func ptr[T any](v T) *T { return &v }
