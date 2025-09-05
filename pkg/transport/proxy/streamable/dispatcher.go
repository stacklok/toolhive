package streamable

import (
	"time"

	"golang.org/x/exp/jsonrpc2"

	"github.com/stacklok/toolhive/pkg/logger"
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
				logger.Warnf("Received invalid message that is not a valid response: %T", resp)
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
							logger.Warnf("Waiter channel full for compositeKey=%s; dropping response", sID)
						}
						continue
					}
				}
				logger.Warnf("No waiter found for compositeKey=%s; dropping", sID)
				continue
			}
			logger.Warnf("Non-string response id (expected composite string); dropping: %v", rawID)
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
