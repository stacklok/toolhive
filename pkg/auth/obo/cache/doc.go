// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package cache adds a session-partitioned, in-memory token cache in front of
// any exchange.TokenExchanger.
//
// Cache decorates a provider (entra.Exchanger today) and implements
// exchange.Exchanger, so the proxy middleware and the vMCP strategy are unaware
// of the caching. Cached tokens are partitioned by (hash(userToken), audience):
// a given user's token for a given audience is reused until it is within a
// configurable skew of its expiry, at which point the next call re-exchanges.
// Different user tokens and different audiences never collide.
//
// Typical wiring:
//
//	exchanger, err := entra.New(cfg)
//	if err != nil { ... }
//	var oboExchanger exchange.Exchanger = cache.New(exchanger)
//
// The package links only the standard library and the stdlib-only
// pkg/auth/obo/exchange contract.
package cache
