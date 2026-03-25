// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package gomicrovm

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/stacklok/go-microvm"
)

const (
	readinessInitialDelay = 500 * time.Millisecond
	readinessMaxBackoff   = 2 * time.Second
	readinessTimeout      = 60 * time.Second
)

// httpReadinessProbe returns a PostBootHook that polls the given host port
// until an HTTP response is received (any status code means the server is up).
// It uses exponential backoff starting at 500ms up to 2s, with a total
// timeout of 60s.
func httpReadinessProbe(hostPort int) microvm.PostBootHook {
	return func(ctx context.Context, _ *microvm.VM) error {
		url := fmt.Sprintf("http://127.0.0.1:%d/", hostPort)
		client := &http.Client{Timeout: 2 * time.Second}

		deadline := time.Now().Add(readinessTimeout)
		delay := readinessInitialDelay

		for {
			if time.Now().After(deadline) {
				return fmt.Errorf("readiness probe timed out after %s waiting for %s", readinessTimeout, url)
			}

			req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
			if err != nil {
				return fmt.Errorf("creating readiness request: %w", err)
			}

			resp, err := client.Do(req)
			if err == nil {
				_ = resp.Body.Close()
				slog.Debug("readiness probe succeeded", "url", url, "status", resp.StatusCode)
				return nil
			}

			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay):
			}

			// Exponential backoff.
			delay = delay * 2
			if delay > readinessMaxBackoff {
				delay = readinessMaxBackoff
			}
		}
	}
}
