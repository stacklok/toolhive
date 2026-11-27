// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package strategies

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"slices"
	"sync/atomic"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/auth/upstreamtoken"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
)

// Option configures a strategy's upstream credential resolution.
type Option func(*upstreamCredentials)

// WithUserCredentialReader configures the reader used to look up delegated
// credentials by platform user ID and provider. It does not change token
// resolution on non-delegated routes.
// A nil or typed-nil reader is treated as no reader.
func WithUserCredentialReader(reader upstreamtoken.UserCredentialReader) Option {
	return func(c *upstreamCredentials) {
		if !isNilReader(reader) {
			c.reader = reader
		}
	}
}

// isNilReader reports whether reader is nil or wraps a nil value, which would
// panic on the first delegated read instead of failing closed.
func isNilReader(reader upstreamtoken.UserCredentialReader) bool {
	if reader == nil {
		return true
	}
	v := reflect.ValueOf(reader)
	if slices.Contains([]reflect.Kind{
		reflect.Pointer, reflect.Map, reflect.Func, reflect.Interface, reflect.Slice, reflect.Chan,
	}, v.Kind()) {
		return v.IsNil()
	}
	return false
}

type upstreamCredentials struct {
	reader              upstreamtoken.UserCredentialReader
	missingReaderWarned atomic.Bool
}

func (c *upstreamCredentials) apply(opts ...Option) {
	for _, opt := range opts {
		if opt != nil {
			opt(c)
		}
	}
}

func (c *upstreamCredentials) accessToken(ctx context.Context, identity *auth.Identity, provider string) (string, error) {
	if identity == nil {
		return "", errors.New("no identity for upstream token lookup")
	}

	// Only the delegated route reads through the reader; every other route,
	// including unknown future ones, uses the identity's token map.
	if identity.AdmissionRoute == auth.AdmissionRouteDelegated {
		cred, err := c.readDelegatedCredential(ctx, identity, provider)
		if err != nil {
			return "", err
		}
		if cred.AccessToken == "" {
			slog.WarnContext(ctx, "delegated credential lookup failed", "provider", provider, "reason", "empty_access_token")
			return "", authtypes.ErrDelegatedCredentialUnavailable
		}
		return cred.AccessToken, nil
	}

	token := identity.UpstreamTokens[provider]
	if token == "" {
		return "", fmt.Errorf("provider %q: %w", provider, authtypes.ErrUpstreamTokenNotFound)
	}
	return token, nil
}

func (c *upstreamCredentials) readDelegatedCredential(
	ctx context.Context, identity *auth.Identity, provider string,
) (*upstreamtoken.UpstreamCredential, error) {
	switch {
	case provider == "":
		slog.DebugContext(ctx, "delegated credential provider is empty")
		return nil, authtypes.ErrDelegatedCredentialUnavailable
	case c.reader == nil:
		if c.missingReaderWarned.CompareAndSwap(false, true) {
			slog.WarnContext(ctx, "delegated credential reader is not configured; failing closed on delegated requests",
				"provider", provider)
		} else {
			slog.DebugContext(ctx, "delegated credential reader is not configured", "provider", provider)
		}
		return nil, authtypes.ErrDelegatedCredentialUnavailable
	case identity.PlatformUserID == "":
		slog.ErrorContext(ctx, "delegated identity missing platform user ID", "provider", provider)
		return nil, authtypes.ErrDelegatedCredentialUnavailable
	}

	cred, err := c.reader.GetValidTokensForUser(ctx, identity.PlatformUserID, provider)
	// A reader's own timeout is not the caller's deadline.
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}
	switch {
	case errors.Is(err, upstreamtoken.ErrNeedsAuthorization):
		return nil, authtypes.NewDelegatedAuthRequiredError(provider, upstreamtoken.ErrNeedsAuthorization)
	case errors.Is(err, upstreamtoken.ErrInvalidBinding):
		slog.ErrorContext(ctx, "delegated credential lookup has invalid binding", "provider", provider)
	case errors.Is(err, upstreamtoken.ErrSessionNotFound), errors.Is(err, upstreamtoken.ErrNoCanonicalUser),
		errors.Is(err, upstreamtoken.ErrNoRefreshToken):
		// Persistent per-user conditions; WARN would repeat on every request.
		slog.DebugContext(ctx, "delegated credential lookup failed", "provider", provider, "reason", readerFailureReason(err))
	case err != nil:
		slog.WarnContext(ctx, "delegated credential lookup failed", "provider", provider, "reason", readerFailureReason(err))
	case cred == nil:
		slog.WarnContext(ctx, "delegated credential lookup failed", "provider", provider, "reason", "no_credential_returned")
	default:
		return cred, nil
	}
	return nil, authtypes.ErrDelegatedCredentialUnavailable
}

// readerFailureReason maps a reader error to a fixed category. Reader error
// text can include provider-supplied detail, so it must never be logged.
func readerFailureReason(err error) string {
	switch {
	case errors.Is(err, upstreamtoken.ErrSessionNotFound):
		return "user_not_found"
	case errors.Is(err, upstreamtoken.ErrNoCanonicalUser):
		return "no_canonical_user"
	case errors.Is(err, upstreamtoken.ErrRefreshFailed):
		return "refresh_failed"
	case errors.Is(err, upstreamtoken.ErrNoRefreshToken):
		return "no_refresh_token"
	default:
		return "storage_or_infrastructure"
	}
}
