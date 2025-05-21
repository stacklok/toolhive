package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// DocsRouter creates a new router for documentation endpoints.
func DocsRouter() http.Handler {
	r := chi.NewRouter()
	r.Get("/openapi.json", ServeOpenAPI)
	r.Get("/doc", ServeScalar)
	return r
}
