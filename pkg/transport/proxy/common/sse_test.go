package common

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSetSSEHeaders(t *testing.T) {
	t.Parallel()

	t.Run("sets all required SSE headers", func(t *testing.T) {
		t.Parallel()

		rec := httptest.NewRecorder()
		SetSSEHeaders(rec)

		if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
			t.Errorf("Expected Content-Type to be text/event-stream, got %s", ct)
		}

		if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
			t.Errorf("Expected Cache-Control to be no-cache, got %s", cc)
		}

		if conn := rec.Header().Get("Connection"); conn != "keep-alive" {
			t.Errorf("Expected Connection to be keep-alive, got %s", conn)
		}
	})

	t.Run("overwrites existing headers", func(t *testing.T) {
		t.Parallel()

		rec := httptest.NewRecorder()
		rec.Header().Set("Content-Type", "application/json")
		rec.Header().Set("Cache-Control", "max-age=3600")
		rec.Header().Set("Connection", "close")

		SetSSEHeaders(rec)

		if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
			t.Errorf("Expected Content-Type to be text/event-stream, got %s", ct)
		}

		if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
			t.Errorf("Expected Cache-Control to be no-cache, got %s", cc)
		}

		if conn := rec.Header().Get("Connection"); conn != "keep-alive" {
			t.Errorf("Expected Connection to be keep-alive, got %s", conn)
		}
	})
}

func TestGetFlusher(t *testing.T) {
	t.Parallel()

	t.Run("returns flusher when ResponseWriter supports it", func(t *testing.T) {
		t.Parallel()

		rec := httptest.NewRecorder()
		flusher, err := GetFlusher(rec)

		if err != nil {
			t.Errorf("Expected no error, got %v", err)
		}

		if flusher == nil {
			t.Error("Expected flusher to be non-nil")
		}
	})

	t.Run("returns error when ResponseWriter doesn't support flushing", func(t *testing.T) {
		t.Parallel()

		// Create a ResponseWriter that doesn't implement http.Flusher
		type nonFlushableWriter struct {
			http.ResponseWriter
		}

		w := &nonFlushableWriter{
			ResponseWriter: httptest.NewRecorder(),
		}

		flusher, err := GetFlusher(w)

		if err == nil {
			t.Error("Expected error, got nil")
		}

		if flusher != nil {
			t.Error("Expected flusher to be nil")
		}

		expectedErr := "response writer does not support flushing"
		if err.Error() != expectedErr {
			t.Errorf("Expected error message %q, got %q", expectedErr, err.Error())
		}
	})
}
