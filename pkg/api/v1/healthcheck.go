package v1

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// HealthcheckRouter sets up healthcheck route.
func HealthcheckRouter() http.Handler {
	r := chi.NewRouter()
	r.Get("/", getHealthcheck)
	return r
}

func getHealthcheck(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusNoContent)
}
