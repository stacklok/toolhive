// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package tokenexchange holds the handlers for the token endpoint's
// token-exchange and JWT-bearer grants. The RFC 8693 token exchange handlers
// validate subject tokens issued by this authorization server and trusted
// external JWTs, and issue tokens from them. The RFC 7523 JWT-bearer and ID-JAG
// redemption handlers validate assertions from trusted issuers. Both groups
// share the trust layer: the issuer and JWKS validation code that checks where
// a token came from.
//
// The package also mints ID-JAGs through one generic core that checks the
// request, selects the subject claims, runs the policy, signs the token and
// writes the response, whatever the subject came from.
package tokenexchange
