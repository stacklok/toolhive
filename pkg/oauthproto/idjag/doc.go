// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package idjag holds the ToolHive rules for ID-JAG, the assertion used by
// Cross App Access (XAA). It follows
// draft-ietf-oauth-identity-assertion-authz-grant-06.
//
// An ID-JAG is a short-lived signed JWT whose "typ" header is
// oauth-id-jag+jwt (draft 3.1, 4.4.1; RFC 8725 Section 3.11). Three roles
// take part. The IdP authorization server issues it. The client obtains it and
// presents it. The resource authorization server redeems it.
//
// Step 1, the exchange. The client sends an RFC 8693 token exchange to the IdP
// with requested_token_type set to the ID-JAG token type, exactly one
// "audience" (draft 4.3, 4.3.3) and a "resource" (draft 4.3), which is
// RECOMMENDED and must be included when the client found the resource through
// RFC 9728 metadata. The response carries issued_token_type set to the ID-JAG
// token type, token_type N_A, and the ID-JAG in the access_token member (draft
// 4.3.4).
//
// Step 2, the redemption. The client sends the ID-JAG to the resource
// authorization server as an RFC 7523 JWT-bearer grant (draft 4.4). The server
// validates it as draft 4.4.1 describes: all of RFC 7521 Section 5.2 plus the
// ID-JAG rules. An ID-JAG is a JWT-bearer assertion profile that is obtained
// through a token exchange. The redemption is not itself a token exchange.
//
// This package holds two functions today:
//
//   - [IsIDJAG] is the typ test. Every access-token validator that shares a
//     signing key with an ID-JAG issuer must call it, so an ID-JAG is never
//     accepted as an access token.
//   - [FromExchange] is the client's sanity check on the IdP response from
//     step 1. It checks that the IdP honoured the request: token types, a
//     parsable JWT, the requested audience and the requested resource. The
//     rule that aud is a single issuer identifier belongs to the resource
//     server (draft 4.4.1). The check here requires aud to be exactly the
//     requested value. Draft 4.3 lets an IdP resolve a URN audience to the
//     issuer identifier, and this check refuses that: ToolHive supports only
//     issuer-identifier audiences here.
//
// The resource server's rules arrive in a later change. Signature checks,
// issuer trust, replay protection and client authentication are product
// concerns. This package never does them.
//
// The wiring lives in pkg/vmcp/auth/strategies/xaa.go (the client) and in
// pkg/authserver/server/tokenexchange (the authorization server).
package idjag
