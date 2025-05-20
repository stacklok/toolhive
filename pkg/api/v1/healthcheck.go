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

//	 getHealthcheck
//		@Summary		Health check
//		@Description	Check if the API is healthy
//		@Tags			system
//		@Success		204	{string}	string	"No Content"
//		@Router			/health [get]
func getHealthcheck(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusNoContent)
}
