// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package strategies

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/auth/upstreamtoken"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
)

// Option configures a strategy's upstream credential resolution.
type Option func(*upstreamCredentials)

// WithUserCredentialReader configures the reader used to look up delegated
// credentials by platform user ID and provider. It does not change token
// resolution on non-delegated routes.
func WithUserCredentialReader(reader upstreamtoken.UserCredentialReader) Option {
	return func(c *upstreamCredentials) {
		c.reader = reader
	}
}

type upstreamCredentials struct {
	reader upstreamtoken.UserCredentialReader
}

func newUpstreamCredentials(opts ...Option) upstreamCredentials {
	var c upstreamCredentials
	for _, opt := range opts {
		if opt != nil {
			opt(&c)
		}
	}
	return c
}

func (c upstreamCredentials) accessToken(ctx context.Context, identity *auth.Identity, provider string) (string, error) {
	if identity == nil {
		return "", errors.New("no identity for upstream token lookup")
	}

	// Unknown future routes stay on the identity-map path, even though the
	// capability cache bypasses them. Only the delegated route uses the reader.
	if identity.AdmissionRoute == auth.AdmissionRouteDelegated {
		cred, err := c.readDelegatedCredential(ctx, identity, provider)
		if err != nil {
			return "", err
		}
		if cred.AccessToken == "" {
			slog.WarnContext(ctx, "delegated credential lookup failed", "provider", provider, "error", "empty access token")
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

func (c upstreamCredentials) readDelegatedCredential(
	ctx context.Context, identity *auth.Identity, provider string,
) (*upstreamtoken.UpstreamCredential, error) {
	switch {
	case identity == nil:
		return nil, authtypes.ErrDelegatedCredentialUnavailable
	case provider == "":
		slog.DebugContext(ctx, "delegated credential provider is empty")
		return nil, authtypes.ErrDelegatedCredentialUnavailable
	case c.reader == nil:
		slog.DebugContext(ctx, "delegated credential reader is not configured", "provider", provider)
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
	case err != nil:
		// The reader contract sanitizes errors for operator logs, not clients.
		slog.WarnContext(ctx, "delegated credential lookup failed", "provider", provider, "error", err)
	case cred == nil:
		slog.WarnContext(ctx, "delegated credential lookup failed", "provider", provider, "error", "no credential returned")
	default:
		return cred, nil
	}
	return nil, authtypes.ErrDelegatedCredentialUnavailable
}
