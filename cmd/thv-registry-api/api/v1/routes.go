// Package v1 provides the REST API handlers for MCP Registry access.
package v1

import (
	"encoding/json"
	"errors"
	"net/http"
	"reflect"

	"github.com/go-chi/chi/v5"
	"gopkg.in/yaml.v3"

	"github.com/stacklok/toolhive/cmd/thv-registry-api/docs"
	"github.com/stacklok/toolhive/cmd/thv-registry-api/internal/service"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/registry"
	"github.com/stacklok/toolhive/pkg/versions"
)

const (
	// FormatToolhive is the toolhive format for registry responses
	FormatToolhive = "toolhive"
	// FormatUpstream is the upstream MCP registry format
	FormatUpstream = "upstream"
)

// Response models for API consistency

// RegistryInfoResponse represents the registry information response
type RegistryInfoResponse struct {
	Version      string `json:"version"`
	LastUpdated  string `json:"last_updated"`
	Source       string `json:"source"`
	TotalServers int    `json:"total_servers"`
}

// ServerSummaryResponse represents a server in list API responses (summary view)
type ServerSummaryResponse struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Tier        string `json:"tier"`
	Status      string `json:"status"`
	Transport   string `json:"transport"`
	ToolsCount  int    `json:"tools_count"`
}

// EnvVarDetail represents detailed environment variable information
type EnvVarDetail struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Required    bool   `json:"required"`
	Default     string `json:"default,omitempty"`
	Secret      bool   `json:"secret,omitempty"`
}

// ServerDetailResponse represents a server in detail API responses (full view)
type ServerDetailResponse struct {
	Name          string                 `json:"name"`
	Description   string                 `json:"description"`
	Tier          string                 `json:"tier"`
	Status        string                 `json:"status"`
	Transport     string                 `json:"transport"`
	Tools         []string               `json:"tools"`
	EnvVars       []EnvVarDetail         `json:"env_vars,omitempty"`
	Permissions   map[string]interface{} `json:"permissions,omitempty"`
	Metadata      map[string]interface{} `json:"metadata,omitempty"`
	RepositoryURL string                 `json:"repository_url,omitempty"`
	Tags          []string               `json:"tags,omitempty"`
	Args          []string               `json:"args,omitempty"`
	Volumes       map[string]interface{} `json:"volumes,omitempty"`
	Image         string                 `json:"image,omitempty"`
}

// ListServersResponse represents the servers list response
type ListServersResponse struct {
	Servers []ServerSummaryResponse `json:"servers"`
	Total   int                     `json:"total"`
}

// ErrorResponse represents a standardized error response
type ErrorResponse struct {
	Error string `json:"error"`
}

// Routes defines the routes for the registry API with dependency injection
type Routes struct {
	service service.RegistryService
}

// NewRoutes creates a new Routes instance with the provided service
func NewRoutes(svc service.RegistryService) *Routes {
	return &Routes{
		service: svc,
	}
}

// Router creates a new router for the registry API
func Router(svc service.RegistryService) http.Handler {
	routes := NewRoutes(svc)

	r := chi.NewRouter()

	// MCP Registry API v0 compatible endpoints
	r.Post("/publish", routes.publishServer)

	// Registry metadata
	r.Get("/info", routes.getRegistryInfo)

	// OpenAPI specification
	r.Get("/openapi.yaml", serveOpenAPIYAML)

	// Server endpoints
	r.Route("/servers", func(r chi.Router) {
		r.Get("/", routes.listServers)

		// Deployed servers sub-routes (must come before {name} to avoid conflicts)
		r.Route("/deployed", func(r chi.Router) {
			r.Get("/", routes.listDeployedServers)
			r.Get("/{name}", routes.getDeployedServer)
		})

		r.Get("/{name}", routes.getServer)
	})

	return r
}

// getRegistryInfo handles GET /api/v1/registry/info
//
//	@Summary		Get registry information
//	@Description	Get registry metadata including version, last updated time, and total servers
//	@Tags			registry
//	@Accept			json
//	@Produce		json
//	@Param			format	query		string	false	"Response format"	Enums(toolhive,upstream)	default(toolhive)
//	@Success		200		{object}	RegistryInfoResponse
//	@Failure		400		{object}	ErrorResponse
//	@Failure		501		{object}	ErrorResponse
//	@Router			/api/v1/registry/info [get]
func (rr *Routes) getRegistryInfo(w http.ResponseWriter, r *http.Request) {
	// Get format parameter (default to toolhive for now)
	format := r.URL.Query().Get("format")
	if format == "" {
		format = FormatToolhive
	}

	// Support both toolhive and upstream formats
	if format != FormatToolhive && format != FormatUpstream {
		rr.writeErrorResponse(w, "Unsupported format. Supported formats: 'toolhive', 'upstream'", http.StatusBadRequest)
		return
	}

	// Upstream format not implemented yet
	if format == FormatUpstream {
		rr.writeErrorResponse(w, "Upstream format not yet implemented", http.StatusNotImplemented)
		return
	}

	reg, source, err := rr.service.GetRegistry(r.Context())
	if err != nil {
		logger.Errorf("Failed to get registry: %v", err)
		rr.writeErrorResponse(w, "Failed to get registry information", http.StatusInternalServerError)
		return
	}

	// Create toolhive format response
	info := RegistryInfoResponse{
		Version:      reg.Version,
		LastUpdated:  reg.LastUpdated,
		Source:       source,
		TotalServers: len(reg.GetAllServers()),
	}

	rr.writeJSONResponse(w, info)
}

// listServers handles GET /api/v1/registry/servers
//
//	@Summary		List all servers
//	@Description	Get a list of all available MCP servers in the registry
//	@Tags			servers
//	@Accept			json
//	@Produce		json
//	@Param			format	query		string	false	"Response format"	Enums(toolhive,upstream)	default(toolhive)
//	@Success		200		{object}	ListServersResponse
//	@Failure		400		{object}	ErrorResponse
//	@Failure		501		{object}	ErrorResponse
//	@Router			/api/v1/registry/servers [get]
func (rr *Routes) listServers(w http.ResponseWriter, r *http.Request) {
	// Get format parameter (default to toolhive for now)
	format := r.URL.Query().Get("format")
	if format == "" {
		format = FormatToolhive
	}

	// Support both toolhive and upstream formats
	if format != FormatToolhive && format != FormatUpstream {
		rr.writeErrorResponse(w, "Unsupported format. Supported formats: 'toolhive', 'upstream'", http.StatusBadRequest)
		return
	}

	// Upstream format not implemented yet
	if format == FormatUpstream {
		rr.writeErrorResponse(w, "Upstream format not yet implemented", http.StatusNotImplemented)
		return
	}

	servers, err := rr.service.ListServers(r.Context())
	if err != nil {
		logger.Errorf("Failed to list servers: %v", err)
		rr.writeErrorResponse(w, "Failed to list servers", http.StatusInternalServerError)
		return
	}

	// Convert to summary response format
	serverResponses := make([]ServerSummaryResponse, len(servers))
	for i := range servers {
		serverResponses[i] = newServerSummaryResponse(servers[i])
	}

	// Toolhive format response
	response := ListServersResponse{
		Servers: serverResponses,
		Total:   len(servers),
	}

	rr.writeJSONResponse(w, response)
}

// getServer handles GET /api/v1/registry/servers/{name}
//
//	@Summary		Get server by name
//	@Description	Get detailed information about a specific MCP server
//	@Tags			servers
//	@Accept			json
//	@Produce		json
//	@Param			name	path		string	true	"Server name"
//	@Param			format	query		string	false	"Response format"	Enums(toolhive,upstream)	default(toolhive)
//	@Success		200		{object}	ServerDetailResponse
//	@Failure		400		{object}	ErrorResponse
//	@Failure		404		{object}	ErrorResponse
//	@Failure		501		{object}	ErrorResponse
//	@Router			/api/v1/registry/servers/{name} [get]
func (rr *Routes) getServer(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if name == "" {
		rr.writeErrorResponse(w, "Server name is required", http.StatusBadRequest)
		return
	}

	// Get format parameter (default to toolhive for now)
	format := r.URL.Query().Get("format")
	if format == "" {
		format = FormatToolhive
	}

	// Support both toolhive and upstream formats
	if format != FormatToolhive && format != FormatUpstream {
		rr.writeErrorResponse(w, "Unsupported format. Supported formats: 'toolhive', 'upstream'", http.StatusBadRequest)
		return
	}

	// Upstream format not implemented yet
	if format == FormatUpstream {
		rr.writeErrorResponse(w, "Upstream format not yet implemented", http.StatusNotImplemented)
		return
	}

	server, err := rr.service.GetServer(r.Context(), name)
	if err != nil {
		if errors.Is(err, service.ErrServerNotFound) {
			rr.writeErrorResponse(w, "Server not found", http.StatusNotFound)
			return
		}
		logger.Errorf("Failed to get server %s: %v", name, err)
		rr.writeErrorResponse(w, "Failed to get server", http.StatusInternalServerError)
		return
	}

	// Convert to detailed response format
	serverResponse := newServerDetailResponse(server)

	// Toolhive format
	rr.writeJSONResponse(w, serverResponse)
}

// publishServer handles POST /v0/publish
//
//	@Summary		Publish MCP server (Not Implemented)
//	@Description	Publish a new MCP server to the registry or update an existing one
//	@Tags			servers
//	@Accept			json
//	@Produce		json
//	@Success		501		{object}	ErrorResponse
//	@Router			/v0/publish [post]
func (rr *Routes) publishServer(w http.ResponseWriter, _ *http.Request) {
	rr.writeErrorResponse(w, "Publishing is not supported by this registry implementation", http.StatusNotImplemented)
}

// listDeployedServers handles GET /api/v1/registry/servers/deployed
//
//	@Summary		List deployed servers
//	@Description	Get a list of all currently deployed MCP servers
//	@Tags			deployed-servers
//	@Accept			json
//	@Produce		json
//	@Success		200	{array}		service.DeployedServer
//	@Failure		500	{object}	ErrorResponse
//	@Router			/api/v1/registry/servers/deployed [get]
func (rr *Routes) listDeployedServers(w http.ResponseWriter, r *http.Request) {
	servers, err := rr.service.ListDeployedServers(r.Context())
	if err != nil {
		logger.Errorf("Failed to list deployed servers: %v", err)
		rr.writeErrorResponse(w, "Failed to list deployed servers", http.StatusInternalServerError)
		return
	}

	rr.writeJSONResponse(w, servers)
}

// newServerSummaryResponse creates a ServerSummaryResponse from server metadata
func newServerSummaryResponse(server registry.ServerMetadata) ServerSummaryResponse {
	return ServerSummaryResponse{
		Name:        server.GetName(),
		Description: server.GetDescription(),
		Tier:        server.GetTier(),
		Status:      server.GetStatus(),
		Transport:   server.GetTransport(),
		ToolsCount:  len(server.GetTools()),
	}
}

// newServerDetailResponse creates a ServerDetailResponse from server metadata with all available fields
func newServerDetailResponse(server registry.ServerMetadata) ServerDetailResponse {
	response := ServerDetailResponse{
		Name:          server.GetName(),
		Description:   server.GetDescription(),
		Tier:          server.GetTier(),
		Status:        server.GetStatus(),
		Transport:     server.GetTransport(),
		Tools:         server.GetTools(),
		RepositoryURL: server.GetRepositoryURL(),
		Tags:          server.GetTags(),
	}

	populateEnvVars(&response, server)
	populateMetadata(&response, server)
	populateServerTypeSpecificFields(&response, server)

	return response
}

// populateEnvVars converts and populates environment variables in the response
func populateEnvVars(response *ServerDetailResponse, server registry.ServerMetadata) {
	envVars := server.GetEnvVars()
	if envVars == nil {
		return
	}

	response.EnvVars = make([]EnvVarDetail, 0, len(envVars))
	for _, envVar := range envVars {
		if envVar != nil {
			response.EnvVars = append(response.EnvVars, EnvVarDetail{
				Name:        envVar.Name,
				Description: envVar.Description,
				Required:    envVar.Required,
				Default:     envVar.Default,
				Secret:      envVar.Secret,
			})
		}
	}
}

// populateMetadata converts and populates metadata in the response
func populateMetadata(response *ServerDetailResponse, server registry.ServerMetadata) {
	// Convert metadata from *Metadata to map[string]interface{}
	if metadata := server.GetMetadata(); metadata != nil {
		response.Metadata = map[string]interface{}{
			"stars":        metadata.Stars,
			"pulls":        metadata.Pulls,
			"last_updated": metadata.LastUpdated,
		}
	}

	// Add custom metadata
	if customMetadata := server.GetCustomMetadata(); customMetadata != nil {
		if response.Metadata == nil {
			response.Metadata = make(map[string]interface{})
		}
		for k, v := range customMetadata {
			response.Metadata[k] = v
		}
	}
}

// populateServerTypeSpecificFields populates fields specific to container or remote servers
func populateServerTypeSpecificFields(response *ServerDetailResponse, server registry.ServerMetadata) {
	if !server.IsRemote() {
		populateContainerServerFields(response, server)
	} else {
		populateRemoteServerFields(response, server)
	}
}

// populateContainerServerFields populates fields specific to container servers (ImageMetadata)
func populateContainerServerFields(response *ServerDetailResponse, server registry.ServerMetadata) {
	// The server might be wrapped in a serverWithName struct from the service layer
	actualServer := extractEmbeddedServerMetadata(server)

	// Type assert to access ImageMetadata-specific fields
	imgMetadata, ok := actualServer.(*registry.ImageMetadata)
	if !ok {
		return
	}

	// Add permissions if available
	if imgMetadata.Permissions != nil {
		response.Permissions = map[string]interface{}{
			"profile": imgMetadata.Permissions,
		}
	}

	// Add args if available
	if imgMetadata.Args != nil {
		response.Args = imgMetadata.Args
	}

	// Add image as top-level field
	response.Image = imgMetadata.Image

	// Add image-specific metadata
	if response.Metadata == nil {
		response.Metadata = make(map[string]interface{})
	}
	response.Metadata["target_port"] = imgMetadata.TargetPort
	response.Metadata["docker_tags"] = imgMetadata.DockerTags
}

// populateRemoteServerFields populates fields specific to remote servers
func populateRemoteServerFields(response *ServerDetailResponse, server registry.ServerMetadata) {
	// The server might be wrapped in a serverWithName struct from the service layer
	actualServer := extractEmbeddedServerMetadata(server)

	remoteMetadata, ok := actualServer.(*registry.RemoteServerMetadata)
	if !ok {
		return
	}

	if response.Metadata == nil {
		response.Metadata = make(map[string]interface{})
	}

	response.Metadata["url"] = remoteMetadata.URL
	if remoteMetadata.Headers != nil {
		response.Metadata["headers_count"] = len(remoteMetadata.Headers)
	}
	response.Metadata["oauth_enabled"] = remoteMetadata.OAuthConfig != nil
}

// extractEmbeddedServerMetadata extracts the embedded ServerMetadata from serverWithName wrapper
func extractEmbeddedServerMetadata(server registry.ServerMetadata) registry.ServerMetadata {
	// Use reflection to check if this is a struct with an embedded ServerMetadata field
	v := reflect.ValueOf(server)
	if v.Kind() == reflect.Ptr {
		v = v.Elem()
	}

	if v.Kind() == reflect.Struct {
		// Look for an embedded field of type registry.ServerMetadata
		for i := 0; i < v.NumField(); i++ {
			field := v.Field(i)
			fieldType := v.Type().Field(i)

			// Check if it's an embedded field (Anonymous) that implements ServerMetadata
			if fieldType.Anonymous && field.CanInterface() {
				if serverMetadata, ok := field.Interface().(registry.ServerMetadata); ok {
					return serverMetadata
				}
			}
		}
	}

	// If not wrapped, return the original server
	return server
}

// getDeployedServer handles GET /api/v1/registry/servers/deployed/{name}
//
//	@Summary		Get deployed servers by registry name
//	@Description	Get all deployed MCP servers that match the specified server registry name
//	@Tags			deployed-servers
//	@Accept			json
//	@Produce		json
//	@Param			name	path		string	true	"Server registry name"
//	@Success		200		{array}		service.DeployedServer
//	@Failure		400		{object}	ErrorResponse
//	@Failure		500		{object}	ErrorResponse
//	@Router			/api/v1/registry/servers/deployed/{name} [get]
func (rr *Routes) getDeployedServer(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if name == "" {
		rr.writeErrorResponse(w, "Server name is required", http.StatusBadRequest)
		return
	}

	servers, err := rr.service.GetDeployedServer(r.Context(), name)
	if err != nil {
		logger.Errorf("Failed to get deployed servers for %s: %v", name, err)
		rr.writeErrorResponse(w, "Failed to get deployed servers", http.StatusInternalServerError)
		return
	}

	rr.writeJSONResponse(w, servers)
}

// HealthRouter creates a router for health check endpoints
func HealthRouter(svc service.RegistryService) http.Handler {
	r := chi.NewRouter()

	r.Get("/health", healthHandler)
	r.Get("/readiness", readinessHandler(svc))
	r.Get("/version", versionHandler)

	return r
}

// healthHandler handles health check requests
//
//	@Summary		Health check
//	@Description	Check if the registry API is healthy
//	@Tags			system
//	@Produce		json
//	@Success		200	{object}	map[string]string
//	@Router			/health [get]
func healthHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"healthy"}`))
}

// readinessHandler handles readiness check requests
//
//	@Summary		Readiness check
//	@Description	Check if the registry API is ready to serve requests
//	@Tags			system
//	@Produce		json
//	@Success		200	{object}	map[string]string
//	@Failure		503	{object}	ErrorResponse
//	@Router			/readiness [get]
func readinessHandler(svc service.RegistryService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := svc.CheckReadiness(r.Context()); err != nil {
			errorResp := ErrorResponse{
				Error: "RegistryService not ready: " + err.Error(),
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			if encodeErr := json.NewEncoder(w).Encode(errorResp); encodeErr != nil {
				logger.Errorf("Failed to encode readiness error response: %v", encodeErr)
			}
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ready"}`))
	}
}

// versionHandler handles version information requests
//
//	@Summary		Version information
//	@Description	Get version information about the registry API
//	@Tags			system
//	@Produce		json
//	@Success		200	{object}	map[string]string
//	@Router			/version [get]
func versionHandler(w http.ResponseWriter, _ *http.Request) {
	info := versions.GetVersionInfo()

	response := map[string]string{
		"version":    info.Version,
		"commit":     info.Commit,
		"build_date": info.BuildDate,
		"go_version": info.GoVersion,
		"platform":   info.Platform,
	}

	if err := json.NewEncoder(w).Encode(response); err != nil {
		logger.Errorf("Failed to encode version info: %v", err)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
}

// writeJSONResponse writes a JSON response with the given data
func (*Routes) writeJSONResponse(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	if err := json.NewEncoder(w).Encode(data); err != nil {
		logger.Errorf("Failed to encode JSON response: %v", err)
	}
}

// writeErrorResponse writes a standardized error response
func (*Routes) writeErrorResponse(w http.ResponseWriter, message string, statusCode int) {
	errorResp := ErrorResponse{
		Error: message,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)

	if err := json.NewEncoder(w).Encode(errorResp); err != nil {
		logger.Errorf("Failed to encode error response: %v", err)
	}
}

// serveOpenAPIYAML serves the OpenAPI specification in YAML format
//
//	@Summary		Get OpenAPI specification
//	@Description	Returns the OpenAPI specification for the registry API in YAML format
//	@Tags			system
//	@Produce		application/x-yaml
//	@Success		200	{string}	string	"OpenAPI specification in YAML format"
//	@Router			/api/v1/registry/openapi.yaml [get]
func serveOpenAPIYAML(w http.ResponseWriter, _ *http.Request) {
	// Parse the JSON OpenAPI spec
	var openAPISpec map[string]interface{}
	if err := json.Unmarshal([]byte(docs.SwaggerInfo.ReadDoc()), &openAPISpec); err != nil {
		http.Error(w, "Failed to parse OpenAPI specification", http.StatusInternalServerError)
		return
	}

	// Convert to YAML
	yamlData, err := yaml.Marshal(openAPISpec)
	if err != nil {
		http.Error(w, "Failed to convert OpenAPI specification to YAML", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/x-yaml")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(yamlData)
}

// Test helpers - these functions are exported only for testing purposes

// NewServerSummaryResponseForTesting creates a ServerSummaryResponse for testing
func NewServerSummaryResponseForTesting(server registry.ServerMetadata) ServerSummaryResponse {
	return newServerSummaryResponse(server)
}

// NewServerDetailResponseForTesting creates a ServerDetailResponse for testing
func NewServerDetailResponseForTesting(server registry.ServerMetadata) ServerDetailResponse {
	return newServerDetailResponse(server)
}
