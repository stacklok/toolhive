package v1

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/stacklok/toolhive/pkg/client"
	"github.com/stacklok/toolhive/pkg/logger"
)

// ClientRoutes defines the routes for the client API.
type ClientRoutes struct {
	manager client.Manager
}

// ClientRouter creates a new router for the client API.
func ClientRouter(
	manager client.Manager,
) http.Handler {
	routes := ClientRoutes{
		manager: manager,
	}

	r := chi.NewRouter()
	r.Get("/", routes.listClients)
	r.Post("/", routes.registerClient)
	r.Delete("/{name}", routes.unregisterClient)
	return r
}

// listClients
//
//	@Summary		List all clients
//	@Description	List all registered clients in ToolHive
//	@Tags			clients
//	@Produce		json
//	@Success		200	{array}	client.Client
//	@Router			/api/v1beta/clients [get]
func (c *ClientRoutes) listClients(w http.ResponseWriter, _ *http.Request) {
	clients, err := c.manager.ListClients()
	if err != nil {
		logger.Errorf("Failed to list clients: %v", err)
		http.Error(w, "Failed to list clients", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	err = json.NewEncoder(w).Encode(clients)
	if err != nil {
		http.Error(w, "Failed to encode client list", http.StatusInternalServerError)
		return
	}
}

// registerClient
//
//	@Summary		Register a new client
//	@Description	Register a new client with ToolHive
//	@Tags			clients
//	@Accept			json
//	@Produce		json
//	@Param			client	body	createClientRequest	true	"Client to register"
//	@Success		200	{object}	createClientResponse
//	@Failure		400	{string}	string	"Invalid request"
//	@Router			/api/v1beta/clients [post]
func (c *ClientRoutes) registerClient(w http.ResponseWriter, r *http.Request) {
	var newClient createClientRequest
	err := json.NewDecoder(r.Body).Decode(&newClient)
	if err != nil {
		logger.Errorf("Failed to decode request body: %v", err)
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	err = c.manager.RegisterClient(r.Context(), client.Client{
		Name: newClient.Name,
	})
	if err != nil {
		logger.Errorf("Failed to register client: %v", err)
		http.Error(w, "Failed to register client", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	resp := createClientResponse(newClient)
	if err = json.NewEncoder(w).Encode(resp); err != nil {
		http.Error(w, "Failed to marshal server details", http.StatusInternalServerError)
		return
	}
}

// unregisterClient
//
//	@Summary		Unregister a client
//	@Description	Unregister a client from ToolHive
//	@Tags			clients
//	@Param			name	path	string	true	"Client name to unregister"
//	@Success		204
//	@Failure		400	{string}	string	"Invalid request"
//	@Router			/api/v1beta/clients/{name} [delete]
func (c *ClientRoutes) unregisterClient(w http.ResponseWriter, r *http.Request) {
	clientName := chi.URLParam(r, "name")
	if clientName == "" {
		http.Error(w, "Client name is required", http.StatusBadRequest)
		return
	}

	err := c.manager.UnregisterClient(r.Context(), client.Client{
		Name: client.MCPClient(clientName),
	})
	if err != nil {
		logger.Errorf("Failed to unregister client: %v", err)
		http.Error(w, "Failed to unregister client", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

type createClientRequest struct {
	// Name is the type of the client to register.
	Name client.MCPClient `json:"name"`
}

type createClientResponse struct {
	// Name is the type of the client that was registered.
	Name client.MCPClient `json:"name"`
}
