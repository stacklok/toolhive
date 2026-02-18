// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package streamable

import (
	"fmt"
	"log/slog"
	"time"

	"golang.org/x/exp/jsonrpc2"
)

// dispatchResponses routes container responses to the appropriate waiter by request ID.
// Notifications are ignored for streamable HTTP.
func (p *HTTPProxy) dispatchResponses() {
	for {
		select {
		case <-p.shutdownCh:
			return
		case resp := <-p.responseCh:
			// Ignore notifications
			if isNotification(resp) {
				continue
			}
			r, ok := resp.(*jsonrpc2.Response)
			if !ok || !r.ID.IsValid() {
				slog.Warn("Received invalid message that is not a valid response",
					"type", fmt.Sprintf("%T", resp))
				continue
			}

			rawID := r.ID.Raw()
			// Composite-only routing: responses must carry composite ID (sessID|idKey)
			if sID, ok := rawID.(string); ok {
				if chVal, ok := p.waiters.Load(sID); ok {
					if ch, ok := chVal.(chan jsonrpc2.Message); ok {
						select {
						case ch <- resp:
						default:
							slog.Warn("Waiter channel full; dropping response",
								"composite_key", sID)
						}
						continue
					}
				}
				slog.Warn("No waiter found for composite key; dropping", "composite_key", sID)
				continue
			}
			slog.Warn("Non-string response id (expected composite string); dropping",
				"raw_id", fmt.Sprintf("%v", rawID))
		}
	}
}

// waitForResponse waits for a response on the given channel with timeout.
func (p *HTTPProxy) waitForResponse(ch <-chan jsonrpc2.Message, timeout time.Duration) jsonrpc2.Message {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case msg := <-ch:
		return msg
	case <-timer.C:
		return nil
	case <-p.shutdownCh:
		return nil
	}
}
