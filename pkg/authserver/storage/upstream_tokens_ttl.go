// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package storage

import "time"

func upstreamTokenStorageExpiresAt(now time.Time, tokens *UpstreamTokens) time.Time {
	if tokens != nil {
		if tokens.RefreshToken != "" {
			return now.Add(DefaultUpstreamInactivityTimeout)
		}
		if !tokens.ExpiresAt.IsZero() {
			return tokens.ExpiresAt
		}
	}

	return now.Add(DefaultAccessTokenTTL)
}

func upstreamTokenStorageTTL(now time.Time, tokens *UpstreamTokens) time.Duration {
	ttl := upstreamTokenStorageExpiresAt(now, tokens).Sub(now)
	if ttl <= 0 {
		return time.Millisecond
	}
	return ttl
}
