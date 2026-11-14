// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"context"

	"github.com/ory/fosite"

	oauthserver "github.com/stacklok/toolhive/pkg/authserver/server"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
)

// BuildProviderForTest composes the fosite provider newServer would build for
// cfg on stor, so the external test package can inspect its grant handlers. It
// shares newServer's parameter construction, but skips validation, storage
// preparation and SPIFFE wiring.
func BuildProviderForTest(ctx context.Context, cfg Config, stor storage.Storage) (fosite.OAuth2Provider, error) {
	if err := cfg.applyDefaults(); err != nil {
		return nil, err
	}
	signingKey, additionalPublicKeys, err := loadKeyMaterial(ctx, cfg.KeyProvider)
	if err != nil {
		return nil, err
	}
	authServerConfig, err := oauthserver.NewAuthorizationServerConfig(
		authorizationServerParams(cfg, signingKey, additionalPublicKeys, nil, nil, stor))
	if err != nil {
		return nil, err
	}
	// A nil refresher leaves the upstream refresh guard out. The guard would sit
	// inside the refreshGrantValidators wrapper, which is the one handler the
	// tests that use this helper can see.
	provider, validator, err := buildProvider(cfg, authServerConfig, stor, nil, nil)
	if err != nil {
		return nil, err
	}
	if validator != nil {
		_ = validator.Close()
	}
	return provider, nil
}
