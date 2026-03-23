// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package v1

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	rt "github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/server/discovery"
)

// HealthcheckRouter sets up healthcheck route.
// The nonce parameter, when non-empty, is returned via the X-Toolhive-Nonce
// header so clients can verify they are talking to the expected server instance.
func HealthcheckRouter(containerRuntime rt.Runtime, nonce string) http.Handler {
	routes := &healthcheckRoutes{containerRuntime: containerRuntime, nonce: nonce}
	r := chi.NewRouter()
	r.Get("/", routes.getHealthcheck)
	return r
}

type healthcheckRoutes struct {
	containerRuntime rt.Runtime
	nonce            string
}

//	 getHealthcheck
//		@Summary		Health check
//		@Description	Check if the API is healthy
//		@Tags			system
//		@Success		204	{string}	string	"No Content"
//		@Router			/health [get]
func (h *healthcheckRoutes) getHealthcheck(w http.ResponseWriter, r *http.Request) {
	if err := h.containerRuntime.IsRunning(r.Context()); err != nil {
		// If the container runtime is not running, we return a 503 Service Unavailable status.
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	// Return the server nonce so clients can verify instance identity.
	if h.nonce != "" {
		w.Header().Set(discovery.NonceHeader, h.nonce)
	}
	// If the container runtime is running, we consider the API healthy.
	w.WriteHeader(http.StatusNoContent)
}
