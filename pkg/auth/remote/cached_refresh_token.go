// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package remote

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const cachedRefreshTokenEnvelopeVersion = 1

// errLegacyCachedRefreshToken marks a decode failure caused by the cached
// value not being JSON at all — i.e. a bare refresh token cached before the
// envelope format existed, as opposed to a corrupted or tampered envelope.
var errLegacyCachedRefreshToken = errors.New("legacy cached refresh token: not in envelope format")

// cachedRefreshTokenEnvelope binds a refresh token to the authorization server
// and token endpoint that issued it. It is intentionally stored in the secret
// manager as one opaque value so existing public persistence APIs remain stable.
type cachedRefreshTokenEnvelope struct {
	Version  int    `json:"version"`
	Token    string `json:"token"`
	Issuer   string `json:"issuer"`
	TokenURL string `json:"token_url"`
}

func encodeCachedRefreshToken(token, issuer, tokenURL string) (string, error) {
	if token == "" || issuer == "" || tokenURL == "" {
		return "", fmt.Errorf("cached refresh token envelope requires token, issuer, and token endpoint")
	}

	envelope, err := json.Marshal(cachedRefreshTokenEnvelope{
		Version:  cachedRefreshTokenEnvelopeVersion,
		Token:    token,
		Issuer:   issuer,
		TokenURL: tokenURL,
	})
	if err != nil {
		return "", fmt.Errorf("marshal cached refresh token envelope: %w", err)
	}
	return string(envelope), nil
}

func decodeCachedRefreshToken(value string) (*cachedRefreshTokenEnvelope, error) {
	var envelope cachedRefreshTokenEnvelope
	if err := json.Unmarshal([]byte(value), &envelope); err != nil {
		// A value that isn't even shaped like a JSON object was never an
		// envelope — it's a pre-upgrade bare refresh token. A value that
		// looks like a JSON object but fails to parse is a corrupted or
		// tampered envelope and must not be treated as legacy.
		if !looksLikeJSONObject(value) {
			return nil, fmt.Errorf("%w: %w", errLegacyCachedRefreshToken, err)
		}
		return nil, fmt.Errorf("invalid cached refresh token envelope: %w", err)
	}
	if envelope.Version != cachedRefreshTokenEnvelopeVersion {
		return nil, fmt.Errorf("unsupported cached refresh token envelope version")
	}
	if envelope.Token == "" || envelope.Issuer == "" || envelope.TokenURL == "" {
		return nil, fmt.Errorf("incomplete cached refresh token envelope")
	}
	return &envelope, nil
}

func looksLikeJSONObject(value string) bool {
	return strings.HasPrefix(strings.TrimSpace(value), "{")
}
