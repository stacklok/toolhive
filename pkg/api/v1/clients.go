package v1

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/stacklok/toolhive/pkg/client"
	"github.com/stacklok/toolhive/pkg/config"
	"github.com/stacklok/toolhive/pkg/groups"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/workloads"
)

// ClientRoutes defines the routes for the client API.
type ClientRoutes struct {
	clientManager   client.Manager
	workloadManager workloads.Manager
	groupManager    groups.Manager
}

// ClientRouter creates a new router for the client API.
func ClientRouter(
	manager client.Manager,
	workloadManager workloads.Manager,
	groupManager groups.Manager,
) http.Handler {
	routes := ClientRoutes{
		clientManager:   manager,
		workloadManager: workloadManager,
		groupManager:    groupManager,
	}

	r := chi.NewRouter()
	r.Get("/", routes.listClients)
	r.Post("/", routes.registerClient)
	r.Delete("/{name}", routes.unregisterClient)
	r.Post("/register", routes.registerClientsBulk)
	r.Post("/unregister", routes.unregisterClientsBulk)
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
	clients, err := c.clientManager.ListClients()
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

	// Default groups to "default" group if it exists
	if len(newClient.Groups) == 0 {
		defaultGroup, err := c.groupManager.Get(r.Context(), groups.DefaultGroupName)
		if err != nil {
			logger.Debugf("Failed to get default group: %v", err)
		}
		if defaultGroup != nil {
			newClient.Groups = []string{groups.DefaultGroupName}
		}
	}

	err = c.performClientRegistration(r.Context(), []client.Client{{Name: newClient.Name}}, newClient.Groups)
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

	err := c.clientManager.UnregisterClients(r.Context(), []client.Client{
		{Name: client.MCPClient(clientName)},
	})
	if err != nil {
		logger.Errorf("Failed to unregister client: %v", err)
		http.Error(w, "Failed to unregister client", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// registerClientsBulk
//
//	@Summary		Register multiple clients
//	@Description	Register multiple clients with ToolHive
//	@Tags			clients
//	@Accept			json
//	@Produce		json
//	@Param			clients	body	bulkClientRequest	true	"Clients to register"
//	@Success		200	{array}	createClientResponse
//	@Failure		400	{string}	string	"Invalid request"
//	@Router			/api/v1beta/clients/register [post]
func (c *ClientRoutes) registerClientsBulk(w http.ResponseWriter, r *http.Request) {
	var req bulkClientRequest
	err := json.NewDecoder(r.Body).Decode(&req)
	if err != nil {
		logger.Errorf("Failed to decode request body: %v", err)
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if len(req.Names) == 0 {
		http.Error(w, "At least one client name is required", http.StatusBadRequest)
		return
	}

	clients := make([]client.Client, len(req.Names))
	for i, name := range req.Names {
		clients[i] = client.Client{Name: name}
	}

	err = c.performClientRegistration(r.Context(), clients, req.Groups)
	if err != nil {
		logger.Errorf("Failed to register clients: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	responses := make([]createClientResponse, len(req.Names))
	for i, name := range req.Names {
		responses[i] = createClientResponse{Name: name}
	}

	w.Header().Set("Content-Type", "application/json")
	if err = json.NewEncoder(w).Encode(responses); err != nil {
		http.Error(w, "Failed to marshal response", http.StatusInternalServerError)
		return
	}
}

// unregisterClientsBulk
//
//	@Summary		Unregister multiple clients
//	@Description	Unregister multiple clients from ToolHive
//	@Tags			clients
//	@Accept			json
//	@Param			clients	body	bulkClientRequest	true	"Clients to unregister"
//	@Success		204
//	@Failure		400	{string}	string	"Invalid request"
//	@Router			/api/v1beta/clients/unregister [post]
func (c *ClientRoutes) unregisterClientsBulk(w http.ResponseWriter, r *http.Request) {
	var req bulkClientRequest
	err := json.NewDecoder(r.Body).Decode(&req)
	if err != nil {
		logger.Errorf("Failed to decode request body: %v", err)
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if len(req.Names) == 0 {
		http.Error(w, "At least one client name is required", http.StatusBadRequest)
		return
	}

	// Convert to client.Client slice
	clients := make([]client.Client, len(req.Names))
	for i, name := range req.Names {
		clients[i] = client.Client{Name: name}
	}

	err = c.clientManager.UnregisterClients(r.Context(), clients)
	if err != nil {
		logger.Errorf("Failed to unregister clients: %v", err)
		http.Error(w, "Failed to unregister clients", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

type createClientRequest struct {
	// Name is the type of the client to register.
	Name client.MCPClient `json:"name"`
	// Groups is the list of groups configured on the client.
	Groups []string `json:"groups,omitempty"`
}

type createClientResponse struct {
	// Name is the type of the client that was registered.
	Name client.MCPClient `json:"name"`
	// Groups is the list of groups configured on the client.
	Groups []string `json:"groups,omitempty"`
}

type bulkClientRequest struct {
	// Names is the list of client names to operate on.
	Names []client.MCPClient `json:"names"`
	// Groups is the list of groups configured on the client.
	Groups []string `json:"groups,omitempty"`
}

func (c *ClientRoutes) performClientRegistration(ctx context.Context, clients []client.Client, groupNames []string) error {
	runningWorkloads, err := c.workloadManager.ListWorkloads(ctx, false)
	if err != nil {
		return fmt.Errorf("failed to list running workloads: %w", err)
	}

	if len(groupNames) > 0 {
		logger.Infof("Filtering workloads to groups: %v", groupNames)

		filteredWorkloads, err := workloads.FilterByGroups(ctx, runningWorkloads, groupNames)
		if err != nil {
			return fmt.Errorf("failed to filter workloads by groups: %w", err)
		}

		// Extract client names
		clientNames := make([]string, len(clients))
		for i, clientToRegister := range clients {
			clientNames[i] = string(clientToRegister.Name)
		}

		// Register the clients in the groups
		err = c.groupManager.RegisterClients(ctx, groupNames, clientNames)
		if err != nil {
			return fmt.Errorf("failed to register clients with groups: %w", err)
		}

		// Add the workloads to the client's configuration file
		err = c.clientManager.RegisterClients(clients, filteredWorkloads)
		if err != nil {
			return fmt.Errorf("failed to register clients: %w", err)
		}
	} else {
		// We should never reach this point once groups are enabled
		for _, clientToRegister := range clients {
			err := config.UpdateConfig(func(c *config.Config) {
				for _, registeredClient := range c.Clients.RegisteredClients {
					if registeredClient == string(clientToRegister.Name) {
						logger.Infof("Client %s is already registered, skipping...", clientToRegister.Name)
						return
					}
				}

				c.Clients.RegisteredClients = append(c.Clients.RegisteredClients, string(clientToRegister.Name))
			})
			if err != nil {
				return fmt.Errorf("failed to update configuration for client %s: %w", clientToRegister.Name, err)
			}

			logger.Infof("Successfully registered client: %s\n", clientToRegister.Name)
		}

		err = c.clientManager.RegisterClients(clients, runningWorkloads)
		if err != nil {
			return fmt.Errorf("failed to register clients: %w", err)
		}
	}

	return nil
}
