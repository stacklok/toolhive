// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package entra implements the Microsoft Entra (Azure AD) variant of the
// on-behalf-of (OBO) token exchange.
//
// It performs the Entra OBO flow — the jwt-bearer grant with
// requested_token_use=on_behalf_of against the v2.0 token endpoint — and maps
// the provider's error responses onto a small typed taxonomy (see errors.go).
// Exchanger implements both exchange.Exchanger and exchange.TokenExchanger;
// wrap it in the sibling cache package to add session-partitioned token
// caching:
//
//	exchanger, err := entra.New(entra.Config{
//	    TenantID:     "<tenant>",
//	    ClientID:     "<app-id>",
//	    ClientSecret: "<secret>",
//	})
//	if err != nil { ... }
//	var oboExchanger exchange.Exchanger = cache.New(exchanger)
//
// The package links only the standard library and the stdlib-only
// pkg/auth/obo/exchange contract. It deliberately does NOT depend on the
// oauthproto helpers, which transitively pull in a large dependency graph
// (fosite, ristretto, grpc-gateway) that the proxyrunner does not otherwise
// need, and it stays free of client-go and controller-runtime (see the
// dependency guard test in pkg/auth/obo).
package entra
