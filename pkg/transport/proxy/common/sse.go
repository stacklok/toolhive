package common

import (
	"fmt"
	"net/http"
)

// SetSSEHeaders sets standard Server-Sent Events response headers.
func SetSSEHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
}

// GetFlusher returns a http.Flusher from the ResponseWriter.
// Returns an error if the ResponseWriter doesn't support flushing.
func GetFlusher(w http.ResponseWriter) (http.Flusher, error) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil, fmt.Errorf("response writer does not support flushing")
	}
	return flusher, nil
}
