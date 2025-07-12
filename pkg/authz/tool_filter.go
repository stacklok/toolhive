package authz

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"text/template"

	"github.com/mark3labs/mcp-go/mcp"
	"golang.org/x/exp/jsonrpc2"

	mcpparser "github.com/stacklok/toolhive/pkg/mcp"
)

//go:embed templates
var templateFS embed.FS

// ToolFilteringWriter wraps an http.ResponseWriter to intercept and filter MCP list responses
// based on configured Cedar policies. It supports filtering tools only.
// (restored version)
type ToolFilteringWriter struct {
	http.ResponseWriter
	authorizer    *CedarAuthorizer
	request       *http.Request
	parsedRequest *mcpparser.ParsedMCPRequest
	buffer        *bytes.Buffer
	statusCode    int
}

// NewToolFilteringWriter creates a new response filtering writer
func NewToolFilteringWriter(
	w http.ResponseWriter,
	authorizer *CedarAuthorizer,
	r *http.Request,
	parsedRequest *mcpparser.ParsedMCPRequest,
) *ToolFilteringWriter {
	return &ToolFilteringWriter{
		ResponseWriter: w,
		authorizer:     authorizer,
		request:        r,
		parsedRequest:  parsedRequest,
		buffer:         &bytes.Buffer{},
		statusCode:     http.StatusOK,
	}
}

// Write captures the response body for filtering
func (tfw *ToolFilteringWriter) Write(data []byte) (int, error) {
	return tfw.buffer.Write(data)
}

// WriteHeader captures the status code
func (tfw *ToolFilteringWriter) WriteHeader(statusCode int) {
	tfw.statusCode = statusCode
}

// Flush processes the captured response and applies filtering if needed
func (tfw *ToolFilteringWriter) Flush() error {
	if tfw.statusCode != http.StatusOK && tfw.statusCode != http.StatusAccepted {
		tfw.ResponseWriter.WriteHeader(tfw.statusCode)
		_, err := tfw.ResponseWriter.Write(tfw.buffer.Bytes())
		return err
	}

	// Check if this is a tools list operation that needs filtering
	if tfw.parsedRequest.Method != string(mcp.MethodToolsList) {
		tfw.ResponseWriter.WriteHeader(tfw.statusCode)
		_, err := tfw.ResponseWriter.Write(tfw.buffer.Bytes())
		return err
	}

	rawResponse := tfw.buffer.Bytes()
	if len(rawResponse) == 0 {
		tfw.ResponseWriter.WriteHeader(tfw.statusCode)
		_, err := tfw.ResponseWriter.Write(rawResponse)
		return err
	}

	var response jsonrpc2.Response
	if err := json.Unmarshal(rawResponse, &response); err != nil {
		tfw.ResponseWriter.WriteHeader(tfw.statusCode)
		_, err := tfw.ResponseWriter.Write(rawResponse)
		return err
	}

	filteredResponse, err := tfw.filterListResponse(&response)
	if err != nil {
		return tfw.writeErrorResponse(response.ID, err)
	}

	filteredData, err := json.Marshal(filteredResponse)
	if err != nil {
		return tfw.writeErrorResponse(response.ID, err)
	}

	tfw.ResponseWriter.WriteHeader(tfw.statusCode)
	_, err = tfw.ResponseWriter.Write(filteredData)
	return err
}

// filterListResponse filters tools based on Cedar policies
func (tfw *ToolFilteringWriter) filterListResponse(response *jsonrpc2.Response) (*jsonrpc2.Response, error) {
	if response.Error != nil {
		// If there's an error in the response, don't filter
		return response, nil
	}

	if response.Result == nil {
		// If there's no result, don't filter
		return response, nil
	}

	var listResult mcp.ListToolsResult
	if err := json.Unmarshal(response.Result, &listResult); err != nil {
		// If we can't parse it as a list response, just return it as-is
		return response, nil
	}

	var filteredTools []mcp.Tool

	for _, tool := range listResult.Tools {
		if tfw.authorizer != nil {
			authorized, err := tfw.authorizer.AuthorizeWithJWTClaims(
				tfw.request.Context(),
				MCPFeatureTool,
				MCPOperationCall,
				tool.Name,
				nil, // No arguments for the authorization check
			)
			if err != nil {
				// If there's an error checking authorization, skip this tool
				continue
			}

			if !authorized {
				continue
			}
		}
		filteredTools = append(filteredTools, tool)
	}

	// Create a new result with filtered tools
	filteredResult := mcp.ListToolsResult{
		PaginatedResult: listResult.PaginatedResult,
		Tools:           filteredTools,
	}

	// Marshal the filtered result back
	filteredResultData, err := json.Marshal(filteredResult)
	if err != nil {
		return nil, err
	}

	// Create a new response with the filtered result
	filteredResponse := &jsonrpc2.Response{
		ID:     response.ID,
		Result: json.RawMessage(filteredResultData),
	}

	return filteredResponse, nil
}

// writeErrorResponse writes an error response
func (tfw *ToolFilteringWriter) writeErrorResponse(id jsonrpc2.ID, err error) error {
	errorResponse := &jsonrpc2.Response{
		ID:    id,
		Error: jsonrpc2.NewError(500, fmt.Sprintf("Error filtering response: %v", err)),
	}
	errorData, err := json.Marshal(errorResponse)
	if err != nil {
		return err
	}
	tfw.ResponseWriter.WriteHeader(http.StatusInternalServerError)
	_, err = tfw.ResponseWriter.Write(errorData)
	return err
}

// ToolFilteringMiddleware creates an HTTP middleware that filters MCP list responses based on
// Cedar policies. This middleware intercepts responses
// and filters out items that are not authorized by Cedar policies.
// For tools/call requests, it checks authorization before allowing the request to proceed.
func ToolFilteringMiddleware(
	authorizer *CedarAuthorizer,
) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if shouldSkip(r) {
				next.ServeHTTP(w, r)
				return
			}
			parsedRequest := mcpparser.GetParsedMCPRequest(r.Context())
			if parsedRequest == nil {
				next.ServeHTTP(w, r)
				return
			}

			switch parsedRequest.Method {
			case string(mcp.MethodToolsCall):
				handleToolsCall(w, r, parsedRequest, authorizer, next)
			case string(mcp.MethodToolsList):
				handleToolsList(w, r, parsedRequest, authorizer, next)
			default:
				// For other methods, just pass through
				next.ServeHTTP(w, r)
			}
		})
	}
}

// handleToolsCall handles tools/call requests with authorization checks
func handleToolsCall(
	w http.ResponseWriter,
	r *http.Request,
	parsedRequest *mcpparser.ParsedMCPRequest,
	authorizer *CedarAuthorizer,
	next http.Handler,
) {
	if authorizer != nil {
		// Extract tool name from the request
		toolName := parsedRequest.ResourceID
		if toolName == "" {
			// Try to get tool name from arguments
			if args := parsedRequest.Arguments; args != nil {
				if name, exists := args["name"]; exists {
					if nameStr, ok := name.(string); ok {
						toolName = nameStr
					}
				}
			}
		}

		if toolName != "" {
			// Check if the tool is authorized
			authorized, err := authorizer.AuthorizeWithJWTClaims(
				r.Context(),
				MCPFeatureTool,
				MCPOperationCall,
				toolName,
				nil, // No arguments for the authorization check
			)
			if err != nil {
				// If there's an error checking authorization, deny the request
				writeInvalidParamsResponse(w, parsedRequest.ID, "Authorization check failed")
				return
			}

			if !authorized {
				// Tool is not authorized, return invalid params error
				writeInvalidParamsResponse(w, parsedRequest.ID, "Unauthorized")
				return
			}
		}
	}
	// If authorized or no authorizer, proceed with the request
	next.ServeHTTP(w, r)
}

// handleToolsList handles tools/list requests with response filtering
func handleToolsList(
	w http.ResponseWriter,
	r *http.Request,
	parsedRequest *mcpparser.ParsedMCPRequest,
	authorizer *CedarAuthorizer,
	next http.Handler,
) {
	filteringWriter := NewToolFilteringWriter(w, authorizer, r, parsedRequest)
	next.ServeHTTP(filteringWriter, r)
	if err := filteringWriter.Flush(); err != nil {
		fmt.Printf("Error flushing filtered response: %v\n", err)
	}
}

// writeInvalidParamsResponse writes an invalid params error response
func writeInvalidParamsResponse(w http.ResponseWriter, id any, message string) {
	// Convert ID to jsonrpc2.ID
	var jsonrpcID jsonrpc2.ID
	switch v := id.(type) {
	case string:
		jsonrpcID = jsonrpc2.StringID(v)
	case int:
		jsonrpcID = jsonrpc2.Int64ID(int64(v))
	case int64:
		jsonrpcID = jsonrpc2.Int64ID(v)
	case float64:
		jsonrpcID = jsonrpc2.Int64ID(int64(v))
	default:
		jsonrpcID = jsonrpc2.ID{}
	}

	errorResponse := &jsonrpc2.Response{
		ID:    jsonrpcID,
		Error: jsonrpc2.NewError(-32602, message), // -32602 is "Invalid params" error code
	}
	errorData, err := json.Marshal(errorResponse)
	if err != nil {
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, err = w.Write(errorData)
	if err != nil {
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
}

// CreateToolFilteringAuthorizer creates a Cedar authorizer specifically for tool filtering
// that interpolates the allowed tools into a policy template
func CreateToolFilteringAuthorizer(allowedTools []string) (*CedarAuthorizer, error) {
	if len(allowedTools) == 0 {
		// If no tools specified, return an error
		return nil, fmt.Errorf("no tools specified for filtering")
	}

	// Read the policy template
	templateData, err := templateFS.ReadFile("templates/tool_filtering_policy.cedar")
	if err != nil {
		return nil, fmt.Errorf("failed to read policy template: %w", err)
	}

	// Parse the template
	tmpl, err := template.New("tool_filtering_policy").Parse(string(templateData))
	if err != nil {
		return nil, fmt.Errorf("failed to parse policy template: %w", err)
	}

	// Prepare template data
	data := struct {
		Tools []string
	}{
		Tools: allowedTools,
	}

	// Execute the template
	var policyBuilder strings.Builder
	if err := tmpl.Execute(&policyBuilder, data); err != nil {
		return nil, fmt.Errorf("failed to execute policy template: %w", err)
	}

	// Split the policy into individual permit statements
	policyLines := strings.Split(policyBuilder.String(), "\n")
	var policies []string
	for _, line := range policyLines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "permit(") {
			policies = append(policies, line)
		}
	}

	// Create entities for the allowed tools
	entities := createToolEntities(allowedTools)

	// Create the authorizer with separate policies for each tool
	return NewCedarAuthorizer(CedarAuthorizerConfig{
		Policies:     policies,
		EntitiesJSON: entities,
	})
}

// createToolEntities creates Cedar entities for the allowed tools
func createToolEntities(allowedTools []string) string {
	if len(allowedTools) == 0 {
		return "[]"
	}
	entities := make([]map[string]any, 0, len(allowedTools))
	for _, tool := range allowedTools {
		entities = append(entities, map[string]any{
			"uid": map[string]any{
				"type": "Tool",
				"id":   tool,
			},
			"attrs": map[string]any{"name": tool},
		})
	}
	entitiesJSON, _ := json.Marshal(entities)
	return string(entitiesJSON)
}
