// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package strategies

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/auth/upstreamtoken"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
)

func TestUpstreamCredentialsNilIdentity(t *testing.T) {
	t.Parallel()

	creds := upstreamCredentials{reader: unexpectedCredentialReader(t)}
	token, err := creds.accessToken(t.Context(), nil, "github")
	require.Empty(t, token)
	require.ErrorContains(t, err, "no identity")
}

//nolint:paralleltest // Captures the process-wide default logger.
func TestUpstreamCredentialsDelegatedGuards(t *testing.T) {
	//nolint:paralleltest // Captures the process-wide default logger.
	for _, tt := range []struct {
		name      string
		userID    string
		provider  string
		noReader  bool
		wantLog   string
		wantLevel string
	}{
		{name: "empty provider", userID: "user", wantLog: "provider is empty", wantLevel: "DEBUG"},
		{
			name: "nil reader", userID: "user", provider: "github", noReader: true,
			wantLog: "reader is not configured", wantLevel: "WARN",
		},
		{
			name: "empty platform user ID", provider: "github",
			wantLog: "missing platform user ID", wantLevel: "ERROR",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			logs := captureCredentialLogs(t)
			creds := upstreamCredentials{}
			if !tt.noReader {
				creds.reader = unexpectedCredentialReader(t)
			}
			identity := &auth.Identity{
				PrincipalInfo:  auth.PrincipalInfo{Subject: "not-the-platform-user", PlatformUserID: tt.userID},
				AdmissionRoute: auth.AdmissionRouteDelegated,
				UpstreamTokens: map[string]string{tt.provider: "must-not-use"},
			}

			token, err := creds.accessToken(t.Context(), identity, tt.provider)
			require.Empty(t, token)
			require.Same(t, authtypes.ErrDelegatedCredentialUnavailable, err)
			require.Contains(t, logs.String(), `"level":"`+tt.wantLevel+`"`)
			require.Contains(t, logs.String(), tt.wantLog)
			if tt.noReader {
				token, err = creds.accessToken(t.Context(), identity, tt.provider)
				require.Empty(t, token)
				require.Same(t, authtypes.ErrDelegatedCredentialUnavailable, err)
				require.Equal(t, 1, strings.Count(logs.String(), `"level":"WARN"`))
				require.Contains(t, logs.String(), `"level":"DEBUG"`)
			}
		})
	}
}

//nolint:paralleltest // Captures the process-wide default logger.
func TestUpstreamCredentialsDelegatedReaderResult(t *testing.T) {
	const userID, provider, accessToken = "platform-user", "github", "delegated-access-token"
	identity := &auth.Identity{
		PrincipalInfo:  auth.PrincipalInfo{Subject: "different-subject", PlatformUserID: userID},
		AdmissionRoute: auth.AdmissionRouteDelegated,
		UpstreamTokens: map[string]string{provider: "must-not-use"},
	}
	unavailable := authtypes.ErrDelegatedCredentialUnavailable
	readerCause := &oauth2.RetrieveError{ErrorCode: "invalid_grant"}
	needsAuth := fmt.Errorf("lookup failed: %w: %w", upstreamtoken.ErrNeedsAuthorization, readerCause)
	storageFailure := errors.New("storage unavailable: secret-provider-detail")

	//nolint:paralleltest // Captures the process-wide default logger.
	for _, tt := range []struct {
		name      string
		cred      *upstreamtoken.UpstreamCredential
		readerErr error
		callerErr error
		wantErr   error
		wantLevel string
		wantCause string
	}{
		{name: "valid credential", cred: &upstreamtoken.UpstreamCredential{AccessToken: accessToken}},
		{name: "caller canceled", callerErr: context.Canceled, readerErr: needsAuth, wantErr: context.Canceled},
		{name: "caller deadline exceeded", callerErr: context.DeadlineExceeded, wantErr: context.DeadlineExceeded},
		{
			name: "reader timeout with live caller", readerErr: context.DeadlineExceeded,
			wantErr: unavailable, wantLevel: "WARN", wantCause: "storage_or_infrastructure",
		},
		{name: "needs authorization", readerErr: needsAuth, wantErr: upstreamtoken.ErrNeedsAuthorization},
		{
			name: "invalid binding", readerErr: fmt.Errorf("lookup failed: %w", upstreamtoken.ErrInvalidBinding),
			wantErr: unavailable, wantLevel: "ERROR",
		},
		{
			name: "storage failure", readerErr: storageFailure, wantErr: unavailable,
			wantLevel: "WARN", wantCause: "storage_or_infrastructure",
		},
		{
			name: "refresh failed", readerErr: fmt.Errorf("detail: %w", upstreamtoken.ErrRefreshFailed),
			wantErr: unavailable, wantLevel: "WARN", wantCause: "refresh_failed",
		},
		{
			name: "no refresh token", readerErr: fmt.Errorf("detail: %w", upstreamtoken.ErrNoRefreshToken),
			wantErr: unavailable, wantLevel: "DEBUG", wantCause: "no_refresh_token",
		},
		{
			name: "user not found", readerErr: fmt.Errorf("detail: %w", upstreamtoken.ErrSessionNotFound),
			wantErr: unavailable, wantLevel: "DEBUG", wantCause: "user_not_found",
		},
		{
			name: "no canonical user", readerErr: fmt.Errorf("detail: %w", upstreamtoken.ErrNoCanonicalUser),
			wantErr: unavailable, wantLevel: "DEBUG", wantCause: "no_canonical_user",
		},
		{
			name: "nil credential", wantErr: unavailable, wantLevel: "WARN", wantCause: "no_credential_returned",
		},
		{
			name: "empty access token", cred: &upstreamtoken.UpstreamCredential{},
			wantErr: unavailable, wantLevel: "WARN", wantCause: "empty_access_token",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			logs := captureCredentialLogs(t)
			ctx := t.Context()
			switch tt.callerErr {
			case context.Canceled:
				canceled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = canceled
			case context.DeadlineExceeded:
				expired, cancel := context.WithDeadline(ctx, time.Now().Add(-time.Second))
				t.Cleanup(cancel)
				ctx = expired
			}
			calls := 0
			creds := upstreamCredentials{reader: delegatedReaderFunc(func(
				gotCtx context.Context, gotUser, gotProvider string,
			) (*upstreamtoken.UpstreamCredential, error) {
				calls++
				require.Equal(t, ctx, gotCtx)
				require.Equal(t, userID, gotUser)
				require.Equal(t, provider, gotProvider)
				return tt.cred, tt.readerErr
			})}

			token, err := creds.accessToken(ctx, identity, provider)
			require.Equal(t, 1, calls)
			if tt.wantErr == nil {
				require.NoError(t, err)
				require.Equal(t, accessToken, token)
				return
			}
			require.Empty(t, token)
			if tt.wantErr == upstreamtoken.ErrNeedsAuthorization {
				var authRequired *authtypes.DelegatedAuthRequiredError
				require.ErrorAs(t, err, &authRequired)
				require.Equal(t, provider, authRequired.Provider)
				require.True(t, errors.Unwrap(err) == upstreamtoken.ErrNeedsAuthorization, "expected bare authorization sentinel")
				require.NotErrorIs(t, err, readerCause)
				var retrieveErr *oauth2.RetrieveError
				require.NotErrorAs(t, err, &retrieveErr)
				require.ErrorContains(t, err, provider)
				require.NotContains(t, err.Error(), tt.readerErr.Error())
			} else {
				require.True(t, err == tt.wantErr, "expected bare sentinel %v, got %v", tt.wantErr, err)
			}
			if tt.wantLevel != "" {
				require.Contains(t, logs.String(), `"level":"`+tt.wantLevel+`"`)
				require.Contains(t, logs.String(), `"provider":"github"`)
				if tt.wantCause != "" {
					require.Contains(t, logs.String(), `"reason":"`+tt.wantCause+`"`)
				}
				if tt.readerErr != nil {
					require.NotContains(t, logs.String(), tt.readerErr.Error())
					require.NotContains(t, logs.String(), `"error"`)
				}
			}
		})
	}
}

func TestUpstreamCredentialsNonDelegated(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name  string
		route auth.AdmissionRoute
	}{
		{name: "interactive", route: auth.AdmissionRouteInteractive},
		{name: "unspecified", route: auth.AdmissionRouteUnspecified},
		{name: "unknown future route", route: auth.AdmissionRoute("future")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			identity := &auth.Identity{
				PrincipalInfo:  auth.PrincipalInfo{PlatformUserID: "platform-user"},
				AdmissionRoute: tt.route,
				UpstreamTokens: map[string]string{"github": "identity-token"},
			}
			creds := upstreamCredentials{reader: unexpectedCredentialReader(t)}
			token, err := creds.accessToken(t.Context(), identity, "github")
			require.NoError(t, err)
			require.Equal(t, "identity-token", token)

			token, err = creds.accessToken(t.Context(), identity, "other")
			require.ErrorIs(t, err, authtypes.ErrUpstreamTokenNotFound)
			require.Empty(t, token)
		})
	}
}

func captureCredentialLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &output
}

func unexpectedCredentialReader(t *testing.T) delegatedReaderFunc {
	t.Helper()
	return func(context.Context, string, string) (*upstreamtoken.UpstreamCredential, error) {
		t.Error("unexpected delegated credential read")
		return nil, upstreamtoken.ErrNeedsAuthorization
	}
}

type delegatedReaderFunc func(context.Context, string, string) (*upstreamtoken.UpstreamCredential, error)

func (f delegatedReaderFunc) GetValidTokensForUser(ctx context.Context, userID, provider string) (*upstreamtoken.UpstreamCredential, error) {
	return f(ctx, userID, provider)
}

type userKeyedFakeReader struct {
	userKeyed bool
	calls     int
}

func (r *userKeyedFakeReader) GetValidTokensForUser(context.Context, string, string) (*upstreamtoken.UpstreamCredential, error) {
	r.calls++
	return &upstreamtoken.UpstreamCredential{AccessToken: "user-keyed-token"}, nil
}

func (r *userKeyedFakeReader) UserKeyed() bool { return r.userKeyed }

func delegatedIdentity() *auth.Identity {
	return &auth.Identity{
		PrincipalInfo:  auth.PrincipalInfo{PlatformUserID: "platform-user"},
		AdmissionRoute: auth.AdmissionRouteDelegated,
	}
}

//nolint:paralleltest // Captures the process-wide default logger.
func TestWithUserCredentialReaderRefusesNonUserKeyed(t *testing.T) {
	logs := captureCredentialLogs(t)
	reader := &userKeyedFakeReader{userKeyed: false}
	var creds upstreamCredentials
	creds.apply(WithUserCredentialReader(reader))

	for range 2 {
		token, err := creds.accessToken(t.Context(), delegatedIdentity(), "github")
		require.Empty(t, token)
		require.Same(t, authtypes.ErrDelegatedCredentialUnavailable, err)
	}
	require.Zero(t, reader.calls)
	require.Equal(t, 1, strings.Count(logs.String(), `"level":"WARN"`))
	require.Contains(t, logs.String(), "does not read user-keyed storage; failing closed")
	require.Equal(t, 1, strings.Count(logs.String(), `"level":"DEBUG"`))
}

func TestWithUserCredentialReaderAcceptsUserKeyed(t *testing.T) {
	t.Parallel()

	reader := &userKeyedFakeReader{userKeyed: true}
	var creds upstreamCredentials
	creds.apply(WithUserCredentialReader(reader))

	token, err := creds.accessToken(t.Context(), delegatedIdentity(), "github")
	require.NoError(t, err)
	require.Equal(t, "user-keyed-token", token)
	require.Equal(t, 1, reader.calls)
}

func TestWithUserCredentialReaderSessionKeyedService(t *testing.T) {
	t.Parallel()

	stor := storage.NewMemoryStorage()
	t.Cleanup(func() { _ = stor.Close() })
	var creds upstreamCredentials
	creds.apply(WithUserCredentialReader(upstreamtoken.NewInProcessService(stor, nil)))

	token, err := creds.accessToken(t.Context(), delegatedIdentity(), "github")
	require.Empty(t, token)
	require.Same(t, authtypes.ErrDelegatedCredentialUnavailable, err)
}

func TestWithUserCredentialReaderLastOptionWins(t *testing.T) {
	t.Parallel()

	good := &userKeyedFakeReader{userKeyed: true}
	var creds upstreamCredentials
	creds.apply(
		WithUserCredentialReader(&userKeyedFakeReader{}),
		WithUserCredentialReader(good),
		nil,
	)
	token, err := creds.accessToken(t.Context(), delegatedIdentity(), "github")
	require.NoError(t, err)
	require.Equal(t, "user-keyed-token", token)
}

func TestWithUserCredentialReaderNilAndValueReaders(t *testing.T) {
	t.Parallel()

	var typedNil *userKeyedFakeReader
	var creds upstreamCredentials
	creds.apply(WithUserCredentialReader(nil), WithUserCredentialReader(typedNil))
	require.Nil(t, creds.reader)
	require.False(t, creds.readerNotUserKeyed)

	valueReader := delegatedReaderFunc(func(context.Context, string, string) (*upstreamtoken.UpstreamCredential, error) {
		return &upstreamtoken.UpstreamCredential{AccessToken: "value-token"}, nil
	})
	var withValue upstreamCredentials
	withValue.apply(WithUserCredentialReader(valueReader))
	token, err := withValue.accessToken(t.Context(), delegatedIdentity(), "github")
	require.NoError(t, err)
	require.Equal(t, "value-token", token)

	structReader := valueStructReader{}
	var withStruct upstreamCredentials
	withStruct.apply(WithUserCredentialReader(structReader))
	require.NotNil(t, withStruct.reader)
}

type valueStructReader struct{}

func (valueStructReader) GetValidTokensForUser(context.Context, string, string) (*upstreamtoken.UpstreamCredential, error) {
	return nil, upstreamtoken.ErrNeedsAuthorization
}
