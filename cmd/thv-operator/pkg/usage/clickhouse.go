// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package usage

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// httpTimeout bounds a single ClickHouse insert request.
const httpTimeout = 10 * time.Second

// insertQuery instructs ClickHouse to parse the POST body as JSONEachRow rows
// for the usage_events table.
const insertQuery = "INSERT INTO usage_events FORMAT JSONEachRow"

// clickHouseClient POSTs usage snapshots directly into a ClickHouse table over
// HTTP.
//
// WARNING (PoC ONLY): This performs a DIRECT INSERT into ClickHouse from the
// operator. It is acceptable ONLY for a throwaway proof-of-concept. It is UNFIT
// for clusters we do not control: it exposes the ClickHouse endpoint/credentials
// to untrusted environments, has no authentication of the sender, no schema
// validation gateway, no rate limiting, and no abuse protection. A production
// deployment MUST send to an ingestion gateway that we operate, never to
// ClickHouse directly.
type clickHouseClient struct {
	endpoint string
	http     *http.Client
}

// newClickHouseClient builds a client targeting the given ClickHouse HTTP
// endpoint (the base URL, e.g. "https://host:8443/").
func newClickHouseClient(endpoint string) *clickHouseClient {
	return &clickHouseClient{
		endpoint: endpoint,
		http:     &http.Client{Timeout: httpTimeout},
	}
}

// send inserts a single snapshot. It returns an error on any transport or
// non-2xx response; callers must log and continue (never fatal).
func (c *clickHouseClient) send(ctx context.Context, snapshot Snapshot) error {
	body, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("marshal usage snapshot: %w", err)
	}

	endpoint, err := url.Parse(c.endpoint)
	if err != nil {
		return fmt.Errorf("parse ClickHouse endpoint: %w", err)
	}
	// ClickHouse accepts the SQL via the `query` URL param and the rows as the
	// request body.
	q := endpoint.Query()
	q.Set("query", insertQuery)
	endpoint.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build ClickHouse request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("post usage snapshot to ClickHouse: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Read a bounded amount of the response to aid diagnosis without
		// risking unbounded memory on a misbehaving endpoint.
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("ClickHouse insert returned %d: %s", resp.StatusCode, bytes.TrimSpace(snippet))
	}
	return nil
}
