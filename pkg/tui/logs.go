// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"context"
	"strings"
	"time"

	"github.com/stacklok/toolhive/pkg/workloads"
)

const (
	logPollInterval = 500 * time.Millisecond
	logFetchLines   = 50
)

// StreamWorkloadLogs starts a goroutine that polls manager.GetLogs for the
// given workload name and sends new log lines to the returned channel.
// Cancel the context to stop streaming.
func StreamWorkloadLogs(ctx context.Context, manager workloads.Manager, name string) <-chan string {
	ch := make(chan string, 256)
	go func() {
		defer close(ch)
		var lastLines []string
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(logPollInterval):
			}

			raw, err := manager.GetLogs(ctx, name, false, logFetchLines)
			if err != nil {
				continue
			}

			lines := splitLines(raw)
			newLines := diffLines(lastLines, lines)
			lastLines = lines

			for _, l := range newLines {
				select {
				case ch <- l:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return ch
}

// splitLines splits a string into non-empty lines.
func splitLines(s string) []string {
	all := strings.Split(s, "\n")
	out := make([]string, 0, len(all))
	for _, l := range all {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

// diffLines returns lines in next that are not in prev (suffix-based).
// This detects new lines appended since the last poll.
func diffLines(prev, next []string) []string {
	if len(next) == 0 {
		return nil
	}
	if len(prev) == 0 {
		return next
	}

	// Find where next diverges from prev by matching the last known line.
	lastPrev := prev[len(prev)-1]
	for i := len(next) - 1; i >= 0; i-- {
		if next[i] == lastPrev {
			return next[i+1:]
		}
	}

	// No overlap found — return all lines in next.
	return next
}
