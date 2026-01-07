package common

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stacklok/toolhive/pkg/transport/types"
)

func TestApplyMiddlewares(t *testing.T) {
	t.Parallel()

	t.Run("empty middleware slice", func(t *testing.T) {
		t.Parallel()

		handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			if _, err := w.Write([]byte("handler")); err != nil {
				t.Fatal(err)
			}
		})

		result := ApplyMiddlewares(handler)

		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		result.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("Expected status 200, got %d", rec.Code)
		}
		if rec.Body.String() != "handler" {
			t.Errorf("Expected body 'handler', got %s", rec.Body.String())
		}
	})

	t.Run("single middleware", func(t *testing.T) {
		t.Parallel()

		handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if _, err := w.Write([]byte("handler")); err != nil {
				t.Fatal(err)
			}
		})

		middleware := types.NamedMiddleware{
			Name: "test",
			Function: func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if _, err := w.Write([]byte("before-")); err != nil {
						t.Fatal(err)
					}
					next.ServeHTTP(w, r)
					if _, err := w.Write([]byte("-after")); err != nil {
						t.Fatal(err)
					}
				})
			},
		}

		result := ApplyMiddlewares(handler, middleware)

		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		result.ServeHTTP(rec, req)

		expected := "before-handler-after"
		if rec.Body.String() != expected {
			t.Errorf("Expected body %q, got %q", expected, rec.Body.String())
		}
	})

	t.Run("multiple middlewares applied in correct order", func(t *testing.T) {
		t.Parallel()

		handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if _, err := w.Write([]byte("handler")); err != nil {
				t.Fatal(err)
			}
		})

		middleware1 := types.NamedMiddleware{
			Name: "first",
			Function: func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if _, err := w.Write([]byte("1-")); err != nil {
						t.Fatal(err)
					}
					next.ServeHTTP(w, r)
				})
			},
		}

		middleware2 := types.NamedMiddleware{
			Name: "second",
			Function: func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if _, err := w.Write([]byte("2-")); err != nil {
						t.Fatal(err)
					}
					next.ServeHTTP(w, r)
				})
			},
		}

		middleware3 := types.NamedMiddleware{
			Name: "third",
			Function: func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if _, err := w.Write([]byte("3-")); err != nil {
						t.Fatal(err)
					}
					next.ServeHTTP(w, r)
				})
			},
		}

		result := ApplyMiddlewares(handler, middleware1, middleware2, middleware3)

		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		result.ServeHTTP(rec, req)

		// First middleware should be applied first (outermost)
		expected := "1-2-3-handler"
		if rec.Body.String() != expected {
			t.Errorf("Expected body %q, got %q", expected, rec.Body.String())
		}
	})

	t.Run("middleware with state", func(t *testing.T) {
		t.Parallel()

		handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if _, err := w.Write([]byte("handler")); err != nil {
				t.Fatal(err)
			}
		})

		counter := 0
		middleware := types.NamedMiddleware{
			Name: "counter",
			Function: func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					counter++
					next.ServeHTTP(w, r)
				})
			},
		}

		result := ApplyMiddlewares(handler, middleware)

		// Call handler twice
		rec1 := httptest.NewRecorder()
		req1 := httptest.NewRequest(http.MethodGet, "/", nil)
		result.ServeHTTP(rec1, req1)

		rec2 := httptest.NewRecorder()
		req2 := httptest.NewRequest(http.MethodGet, "/", nil)
		result.ServeHTTP(rec2, req2)

		if counter != 2 {
			t.Errorf("Expected counter to be 2, got %d", counter)
		}
	})
}
