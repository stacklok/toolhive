package v1

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/stacklok/toolhive/pkg/config"
	"github.com/stacklok/toolhive/pkg/logger"
)

// ConfigRoutes defines the routes for the config API.
type ConfigRoutes struct {
	configProvider config.Provider
}

// NewConfigRoutes creates a new ConfigRoutes with the default config provider
func NewConfigRoutes() *ConfigRoutes {
	return &ConfigRoutes{
		configProvider: config.NewDefaultProvider(),
	}
}

// NewConfigRoutesWithProvider creates a new ConfigRoutes with a custom config provider
func NewConfigRoutesWithProvider(provider config.Provider) *ConfigRoutes {
	return &ConfigRoutes{
		configProvider: provider,
	}
}

// ConfigRouter creates a new router for the config API.
func ConfigRouter() http.Handler {
	routes := NewConfigRoutes()
	return configRouterWithRoutes(routes)
}

func configRouterWithRoutes(routes *ConfigRoutes) http.Handler {
	r := chi.NewRouter()

	// Usage metrics routes
	r.Route("/usage-metrics", func(r chi.Router) {
		r.Get("/", routes.getUsageMetricsStatus)
		r.Put("/", routes.updateUsageMetricsStatus)
	})

	return r
}

// getUsageMetricsStatus
//
//	@Summary		Get usage metrics status
//	@Description	Get the current status of usage metrics collection
//	@Tags			config
//	@Produce		json
//	@Success		200	{object}	getUsageMetricsStatusResponse
//	@Failure		500	{string}	string	"Internal Server Error"
//	@Router			/api/v1beta/config/usage-metrics [get]
func (c *ConfigRoutes) getUsageMetricsStatus(w http.ResponseWriter, _ *http.Request) {
	enabled := c.configProvider.GetUsageMetricsEnabled()

	w.Header().Set("Content-Type", "application/json")
	resp := getUsageMetricsStatusResponse{
		Enabled: enabled,
	}
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		logger.Errorf("Failed to encode response: %v", err)
		http.Error(w, "Failed to encode response", http.StatusInternalServerError)
		return
	}
}

// updateUsageMetricsStatus
//
//	@Summary		Update usage metrics status
//	@Description	Enable or disable usage metrics collection
//	@Tags			config
//	@Accept			json
//	@Produce		json
//	@Param			request	body		updateUsageMetricsStatusRequest	true	"Update usage metrics status request"
//	@Success		200		{object}	updateUsageMetricsStatusResponse
//	@Failure		400		{string}	string	"Bad Request"
//	@Failure		500		{string}	string	"Internal Server Error"
//	@Router			/api/v1beta/config/usage-metrics [put]
func (c *ConfigRoutes) updateUsageMetricsStatus(w http.ResponseWriter, r *http.Request) {
	var req updateUsageMetricsStatusRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		logger.Errorf("Failed to decode request body: %v", err)
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// Update the usage metrics configuration using the provider method
	err := c.configProvider.SetUsageMetricsEnabled(req.Enabled)
	if err != nil {
		logger.Errorf("Failed to update configuration: %v", err)
		http.Error(w, "Failed to update configuration", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	resp := updateUsageMetricsStatusResponse{
		Enabled: req.Enabled,
		Message: "Usage metrics status updated successfully",
	}
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		logger.Errorf("Failed to encode response: %v", err)
		http.Error(w, "Failed to encode response", http.StatusInternalServerError)
		return
	}
}

// Request and response type definitions

// getUsageMetricsStatusResponse represents the response for getting usage metrics status
//
//	@Description	Response containing usage metrics status
type getUsageMetricsStatusResponse struct {
	// Whether usage metrics collection is enabled
	Enabled bool `json:"enabled"`
}

// updateUsageMetricsStatusRequest represents the request for updating usage metrics status
//
//	@Description	Request to update usage metrics status
type updateUsageMetricsStatusRequest struct {
	// Set to true to enable usage metrics, false to disable
	Enabled bool `json:"enabled"`
}

// updateUsageMetricsStatusResponse represents the response for updating usage metrics status
//
//	@Description	Response after updating usage metrics status
type updateUsageMetricsStatusResponse struct {
	// Whether usage metrics collection is enabled
	Enabled bool `json:"enabled"`
	// Success message
	Message string `json:"message"`
}
