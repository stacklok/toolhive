// Package transparent provides a transparent HTTP proxy implementation
// that forwards requests to a destination without modifying them.
package transparent

import (
	"net/http"

	"github.com/stacklok/toolhive/pkg/transport/types"
)

// ResponseProcessor defines the interface for processing and modifying HTTP responses
// based on transport-specific requirements.
type ResponseProcessor interface {
	// ProcessResponse modifies an HTTP response based on transport-specific logic.
	// Returns error if processing fails.
	ProcessResponse(resp *http.Response) error

	// ShouldProcess returns true if this processor should handle the given response.
	ShouldProcess(resp *http.Response) bool
}

// NoOpResponseProcessor is a processor that does nothing.
// Used for transports that don't require response processing (e.g., streamable-http).
type NoOpResponseProcessor struct{}

// ProcessResponse is a no-op implementation.
func (*NoOpResponseProcessor) ProcessResponse(_ *http.Response) error {
	return nil
}

// ShouldProcess always returns false for no-op processor.
func (*NoOpResponseProcessor) ShouldProcess(_ *http.Response) bool {
	return false
}

// createResponseProcessor is a factory function that creates the appropriate
// response processor based on transport type.
func createResponseProcessor(
	transportType string,
	proxy *TransparentProxy,
	endpointPrefix string,
	trustProxyHeaders bool,
) ResponseProcessor {
	switch transportType {
	case types.TransportTypeSSE.String():
		return NewSSEResponseProcessor(proxy, endpointPrefix, trustProxyHeaders)
	case types.TransportTypeStreamableHTTP.String():
		return &NoOpResponseProcessor{}
	default:
		// Default to no-op for unknown transport types
		return &NoOpResponseProcessor{}
	}
}
