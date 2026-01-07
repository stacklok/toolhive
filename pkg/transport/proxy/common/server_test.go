package common

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestNewHTTPServer(t *testing.T) {
	t.Parallel()

	t.Run("creates server with default ReadHeaderTimeout", func(t *testing.T) {
		t.Parallel()

		handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		})

		config := ServerConfig{
			Host:    "localhost",
			Port:    8080,
			Handler: handler,
		}

		server := NewHTTPServer(config)

		if server.Addr != "localhost:8080" {
			t.Errorf("Expected Addr to be localhost:8080, got %s", server.Addr)
		}

		if server.Handler == nil {
			t.Error("Expected Handler to be non-nil")
		}

		if server.ReadHeaderTimeout != DefaultReadHeaderTimeout {
			t.Errorf("Expected ReadHeaderTimeout to be %v, got %v", DefaultReadHeaderTimeout, server.ReadHeaderTimeout)
		}
	})

	t.Run("creates server with custom ReadHeaderTimeout", func(t *testing.T) {
		t.Parallel()

		handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		})

		customTimeout := 30 * time.Second
		config := ServerConfig{
			Host:              "0.0.0.0",
			Port:              9090,
			Handler:           handler,
			ReadHeaderTimeout: customTimeout,
		}

		server := NewHTTPServer(config)

		if server.Addr != "0.0.0.0:9090" {
			t.Errorf("Expected Addr to be 0.0.0.0:9090, got %s", server.Addr)
		}

		if server.ReadHeaderTimeout != customTimeout {
			t.Errorf("Expected ReadHeaderTimeout to be %v, got %v", customTimeout, server.ReadHeaderTimeout)
		}
	})

	t.Run("formats address correctly for IPv6", func(t *testing.T) {
		t.Parallel()

		handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		})

		config := ServerConfig{
			Host:    "::1",
			Port:    8080,
			Handler: handler,
		}

		server := NewHTTPServer(config)

		if server.Addr != "::1:8080" {
			t.Errorf("Expected Addr to be ::1:8080, got %s", server.Addr)
		}
	})
}

func TestMountHealthCheck(t *testing.T) {
	t.Parallel()

	t.Run("mounts health check when handler is non-nil", func(t *testing.T) {
		t.Parallel()

		mux := http.NewServeMux()
		healthHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			if _, err := w.Write([]byte("healthy")); err != nil {
				t.Fatal(err)
			}
		})

		MountHealthCheck(mux, healthHandler)

		// Test that the health endpoint works
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/health", nil)
		mux.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("Expected status 200, got %d", rec.Code)
		}

		if rec.Body.String() != "healthy" {
			t.Errorf("Expected body 'healthy', got %s", rec.Body.String())
		}
	})

	t.Run("does not mount when handler is nil", func(t *testing.T) {
		t.Parallel()

		mux := http.NewServeMux()
		MountHealthCheck(mux, nil)

		// Test that the health endpoint returns 404
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/health", nil)
		mux.ServeHTTP(rec, req)

		if rec.Code != http.StatusNotFound {
			t.Errorf("Expected status 404, got %d", rec.Code)
		}
	})
}

func TestMountMetrics(t *testing.T) {
	t.Parallel()

	t.Run("mounts metrics and returns true when handler is non-nil", func(t *testing.T) {
		t.Parallel()

		mux := http.NewServeMux()
		metricsHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			if _, err := w.Write([]byte("metrics")); err != nil {
				t.Fatal(err)
			}
		})

		mounted := MountMetrics(mux, metricsHandler)

		if !mounted {
			t.Error("Expected MountMetrics to return true")
		}

		// Test that the metrics endpoint works
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
		mux.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("Expected status 200, got %d", rec.Code)
		}

		if rec.Body.String() != "metrics" {
			t.Errorf("Expected body 'metrics', got %s", rec.Body.String())
		}
	})

	t.Run("does not mount and returns false when handler is nil", func(t *testing.T) {
		t.Parallel()

		mux := http.NewServeMux()
		mounted := MountMetrics(mux, nil)

		if mounted {
			t.Error("Expected MountMetrics to return false")
		}

		// Test that the metrics endpoint returns 404
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
		mux.ServeHTTP(rec, req)

		if rec.Code != http.StatusNotFound {
			t.Errorf("Expected status 404, got %d", rec.Code)
		}
	})
}
