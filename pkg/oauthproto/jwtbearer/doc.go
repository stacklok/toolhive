// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package jwtbearer provides an OAuth 2.0 JWT Bearer Grant (RFC 7523) implementation.
// It exchanges a JWT assertion (such as an ID-JAG) for an access token at a target
// authorization server. It also exposes ValidateTokenURL, a shared token-endpoint
// URL validator reused by other OAuth token-exchange strategies in this repo.
package jwtbearer
