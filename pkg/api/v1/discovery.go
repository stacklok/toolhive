package v1

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	"github.com/stacklok/toolhive/pkg/client"
)

// DiscoveryRoutes defines the routes for the client discovery API.
type DiscoveryRoutes struct {
	logger *zap.SugaredLogger
}

// DiscoveryRouter creates a new router for the client discovery API.
func DiscoveryRouter(logger *zap.SugaredLogger) http.Handler {
	routes := DiscoveryRoutes{logger}

	r := chi.NewRouter()
	r.Get("/clients", routes.discoverClients)
	return r
}

// discoverClients
//
//	@Summary		List all clients status
//	@Description	List all clients compatible with ToolHive and their status
//	@Tags			discovery
//	@Produce		json
//	@Success		200	{object}	clientStatusResponse
//	@Router			/api/v1beta/discovery/clients [get]
func (d *DiscoveryRoutes) discoverClients(w http.ResponseWriter, _ *http.Request) {
	clients, err := client.GetClientStatus(d.logger)
	if err != nil {
		// TODO: Error should be JSON marshaled
		http.Error(w, "Failed to get client status", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	err = json.NewEncoder(w).Encode(clientStatusResponse{Clients: clients})
	if err != nil {
		http.Error(w, "Failed to encode client status", http.StatusInternalServerError)
		return
	}
}

// clientStatusResponse represents the response for the client discovery
type clientStatusResponse struct {
	Clients []client.MCPClientStatus `json:"clients"`
}
