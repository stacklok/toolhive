// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/stacklok/toolhive/pkg/workloads"
)

// proxyLogLineMsg carries a single new proxy log line from the polling goroutine.
type proxyLogLineMsg string

// proxyLogStreamDoneMsg is sent when the proxy log stream channel is closed.
type proxyLogStreamDoneMsg struct{}

// StreamProxyLogs polls manager.GetProxyLogs for the given workload and sends
// new lines to the returned channel. Cancel ctx to stop.
func StreamProxyLogs(ctx context.Context, manager workloads.Manager, name string) <-chan string {
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

			raw, err := manager.GetProxyLogs(ctx, name, logFetchLines)
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

// readProxyLogLine returns a tea.Cmd that waits for the next proxy log line.
func readProxyLogLine(ch <-chan string) tea.Cmd {
	return func() tea.Msg {
		line, ok := <-ch
		if !ok {
			return proxyLogStreamDoneMsg{}
		}
		return proxyLogLineMsg(line)
	}
}
