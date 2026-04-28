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
	logFetchLines   = 500
)

// StreamWorkloadLogs starts a goroutine that polls manager.GetLogs for the
// given workload name and sends new log lines to the returned channel.
// Cancel the context to stop streaming.
func StreamWorkloadLogs(ctx context.Context, manager workloads.Manager, name string) <-chan string {
	ch := make(chan string, 256)
	go func() {
		defer close(ch)
		var lastLines []string
		var errBackoff time.Duration
		const maxBackoff = 5 * time.Second
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(logPollInterval):
			}

			raw, err := manager.GetLogs(ctx, name, false, logFetchLines)
			if err != nil {
				select {
				case ch <- "[error] " + err.Error():
				case <-ctx.Done():
					return
				}
				if errBackoff == 0 {
					errBackoff = time.Second
				} else {
					errBackoff = min(errBackoff*2, maxBackoff)
				}
				select {
				case <-time.After(errBackoff):
				case <-ctx.Done():
					return
				}
				continue
			}
			errBackoff = 0 // reset on success

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
//
// To handle duplicate log lines reliably, we match a suffix of prev (up to
// diffMatchLen lines) as a contiguous sequence in next, rather than matching
// only the last line. This makes false positional matches exponentially
// less likely when the same log message repeats.
func diffLines(prev, next []string) []string {
	if len(next) == 0 {
		return nil
	}
	if len(prev) == 0 {
		return next
	}

	// Take the last few lines of prev as the match sequence.
	matchLen := diffMatchLen
	if matchLen > len(prev) {
		matchLen = len(prev)
	}
	suffix := prev[len(prev)-matchLen:]

	// Scan next from the end looking for the suffix sequence.
	for i := len(next) - matchLen; i >= 0; i-- {
		if slicesEqual(next[i:i+matchLen], suffix) {
			return next[i+matchLen:]
		}
	}

	// No sequence match — fall back to single-line match on the last prev line.
	lastPrev := prev[len(prev)-1]
	for i := len(next) - 1; i >= 0; i-- {
		if next[i] == lastPrev {
			return next[i+1:]
		}
	}

	// No overlap found — return all lines in next.
	return next
}

// diffMatchLen is the number of trailing lines from the previous poll used
// to anchor the suffix match. Higher values reduce false matches with
// duplicate lines but require more overlap to detect the boundary.
const diffMatchLen = 3

// slicesEqual returns true if a and b have the same length and contents.
func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
