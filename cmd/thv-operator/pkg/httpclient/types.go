package httpclient

import "fmt"

// HTTPError represents an HTTP error
type HTTPError struct {
	StatusCode int
	Message    string
	URL        string
}

// Error returns the error message
func (e *HTTPError) Error() string {
	return fmt.Sprintf("HTTP %d for URL %s: %s", e.StatusCode, e.URL, e.Message)
}

// NewHTTPError creates a new HTTP error
func NewHTTPError(statusCode int, url, message string) error {
	return &HTTPError{
		StatusCode: statusCode,
		URL:        url,
		Message:    message,
	}
}
