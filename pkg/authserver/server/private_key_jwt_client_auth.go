// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"

	"github.com/ory/fosite"
)

// clientAssertionAudienceConfig overrides the token URLs fosite accepts as the
// audience of a client assertion. fosite reads them only when authenticating
// clients; every other Configurator method comes from the embedded config.
type clientAssertionAudienceConfig struct {
	*fosite.Config
	tokenURLs []string
}

func (c clientAssertionAudienceConfig) GetTokenURLs(context.Context) []string {
	return c.tokenURLs
}

// newClientAssertionAudienceConfig wraps config so that client assertions may
// carry either the issuer or the token endpoint URL as audience.
func newClientAssertionAudienceConfig(config *fosite.Config) clientAssertionAudienceConfig {
	return clientAssertionAudienceConfig{
		Config:    config,
		tokenURLs: []string{config.AccessTokenIssuer, config.TokenURL},
	}
}
