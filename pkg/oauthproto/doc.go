// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package oauthproto provides shared RFC-defined types, constants, and validation
// utilities for OAuth 2.0 and OpenID Connect. It serves as a shared foundation for
// both OAuth clients and servers.
//
// Surface area:
//   - RFC 8414 authorization server metadata types and well-known paths
//   - Redirect URI validation per RFC 6749 and RFC 8252
//   - RFC 7591 Dynamic Client Registration client-side types: request/response
//     types, ScopeList JSON codec, RegisterClientDynamically, ToolHiveMCPClientName.
//     The authserver hosts its own server-side DCR types in
//     pkg/authserver/server/registration.
//   - Shared constants: UserAgent, well-known paths, grant types, PKCE methods
//
// Reading guide:
//   - oauthproto: constants for grant types, token types and JWT typ values, and
//     shared helpers.
//   - oauthproto/tokenexchange: the RFC 8693 client. It does not depend on the
//     token types it exchanges.
//   - oauthproto/oautherr: token-endpoint error classification, independent of
//     pkg/auth and pkg/authserver. The old pkg/auth/oautherr forwards here.
//   - oauthproto/jwtbearer: the RFC 7523 JWT-bearer client.
//   - oauthproto/idjag: Cross App Access (ID-JAG). Start at its doc.go.
//     FromExchange is the client's check of an IdP response. IsIDJAG is the
//     deny-side typ test. ResourceClaim reads the resource claim.
//
// Leaf-package invariant: this package has no dependency on
// github.com/stacklok/toolhive/pkg/networking. All callers that need both
// networking helpers and oauthproto types must import both packages independently.
package oauthproto
