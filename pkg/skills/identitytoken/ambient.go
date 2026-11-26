// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package identitytoken

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"
)

const (
	// envRequestURL and envRequestToken are set by GitHub Actions when the
	// job has `permissions: id-token: write` — the ambient OIDC credential
	// source. Absence of either means the job doesn't have that permission,
	// not a failure.
	envRequestURL   = "ACTIONS_ID_TOKEN_REQUEST_URL"
	envRequestToken = "ACTIONS_ID_TOKEN_REQUEST_TOKEN"

	sigstoreAudience = "sigstore"

	ambientRequestTimeout = 10 * time.Second
)

// Ambient fetches a GitHub Actions ambient OIDC token scoped to the
// sigstore audience. ok is false with a nil error when the environment
// doesn't carry the id-token: write request variables — that is simply not
// running under that permission, not a failure. A request that fails once
// both variables are present IS an error: the caller expected this to work.
func Ambient(ctx context.Context) (token string, ok bool, err error) {
	reqURL := os.Getenv(envRequestURL)
	reqToken := os.Getenv(envRequestToken)
	if reqURL == "" || reqToken == "" {
		return "", false, nil
	}

	u, err := url.Parse(reqURL)
	if err != nil {
		return "", false, fmt.Errorf("parsing %s: %w", envRequestURL, err)
	}
	// Merge into the existing query rather than concatenating — the URL GHA
	// provides already carries its own query parameters.
	q := u.Query()
	q.Set("audience", sigstoreAudience)
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", false, fmt.Errorf("building ambient OIDC token request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+reqToken)

	client := &http.Client{Timeout: ambientRequestTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return "", false, fmt.Errorf("requesting ambient OIDC token: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		return "", false, fmt.Errorf("ambient OIDC token request returned status %d", resp.StatusCode)
	}

	var body struct {
		Value string `json:"value"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", false, fmt.Errorf("decoding ambient OIDC token response: %w", err)
	}
	if body.Value == "" {
		return "", false, errors.New("ambient OIDC token response had an empty value")
	}
	return body.Value, true, nil
}
