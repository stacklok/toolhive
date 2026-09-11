// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package deviceflow

import (
	"fmt"
	"time"

	"github.com/ory/fosite"
	"github.com/ory/fosite/handler/oauth2"

	"github.com/stacklok/toolhive/pkg/authserver/server"
	authstorage "github.com/stacklok/toolhive/pkg/authserver/storage"
)

// Factory returns a server.Factory that registers the RFC 8628 device-code
// grant, mirroring how the tokenexchange and jwtbearer factories are
// constructed in server_impl.go's buildProvider.
func Factory(deviceStorage authstorage.DeviceCodeStorage, minInterval time.Duration) server.Factory {
	return func(config *server.AuthorizationServerConfig, stor fosite.Storage, strategy any) (any, error) {
		coreStorage, ok := stor.(oauth2.CoreStorage)
		if !ok {
			return nil, fmt.Errorf("deviceflow: storage backend %T does not implement oauth2.CoreStorage", stor)
		}
		coreStrategy, ok := strategy.(oauth2.CoreStrategy)
		if !ok {
			return nil, fmt.Errorf("deviceflow: strategy %T does not implement oauth2.CoreStrategy", strategy)
		}
		return &Handler{
			DeviceStorage: deviceStorage,
			CoreStorage:   coreStorage,
			Strategy:      coreStrategy,
			// The embedded *fosite.Config, not config itself:
			// AuthorizationServerConfig's own no-context adapter methods of the
			// same name shadow the ctx-taking ones fosite's provider interfaces
			// require (see tokenexchange.Factory's identical concern).
			Config:      config.Config,
			MinInterval: minInterval,
		}, nil
	}
}
