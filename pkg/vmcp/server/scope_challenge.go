// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/stacklok/toolhive-core/mcpcompat/mcp"
	"github.com/stacklok/toolhive/pkg/auth"
	mcpparser "github.com/stacklok/toolhive/pkg/mcp"
	"github.com/stacklok/toolhive/pkg/oauthproto"
	transportsession "github.com/stacklok/toolhive/pkg/transport/session"
	"github.com/stacklok/toolhive/pkg/vmcp"
)

// Legacy SDK handlers cannot set HTTP headers, so scope refusals are rendered
// before dispatch, inside session ownership. Modern dispatch uses the same renderer.
func (s *Server) scopeChallengeMiddleware(next http.Handler) http.Handler {
	if s.config.ScopeAuthorization == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parsed := mcpparser.GetParsedMCPRequest(r.Context())
		if r.Method != http.MethodPost || parsed == nil || !parsed.IsRequest || parsed.ID == nil ||
			parsed.Method != "tools/call" || hasNonObjectArguments(parsed.Params) {
			next.ServeHTTP(w, r)
			return
		}
		identity, _ := auth.IdentityFromContext(r.Context())
		checkErr := s.core.CheckToolCall(r.Context(), identity, parsed.ResourceID, parsed.Arguments)
		var refusal *vmcp.InsufficientScopeError
		if errors.As(checkErr, &refusal) {
			client, err := s.legacyScopeClient(r)
			if err != nil {
				if errors.Is(err, transportsession.ErrSessionNotFound) {
					http.Error(w, "Session terminated", http.StatusNotFound)
				} else {
					http.Error(w, "Session store unavailable", http.StatusServiceUnavailable)
				}
				return
			}
			s.writeScopeChallenge(r.Context(), w, parsed.ID, checkErr, false, client)
			return
		}
		if errors.Is(checkErr, vmcp.ErrAuthorizationFailed) {
			writeModernDenied(w, parsed.ID, vmcp.DenyMessageToolCall)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) writeScopeChallenge(
	ctx context.Context, w http.ResponseWriter, id any, err error, modern bool, client ScopeClientProfile,
) bool {
	var refusal *vmcp.InsufficientScopeError
	if s.config == nil || s.config.ScopeAuthorization == nil || !errors.As(err, &refusal) {
		return false
	}
	metadata, metadataErr := scopeMetadataURL(s.config.ScopeChallenge.ResourceURL)
	if metadataErr != nil {
		http.Error(w, "invalid scope challenge configuration", http.StatusInternalServerError)
		return true
	}
	challenge := fmt.Sprintf(`Bearer error="insufficient_scope", error_description="Additional scopes required", `+
		`scope="%s", resource_metadata="%s"`,
		auth.EscapeQuotes(strings.Join(refusal.RequiredScopes, " ")), auth.EscapeQuotes(metadata))
	w.Header().Set("Cache-Control", "no-store")
	if marker, ok := mcpparser.AuthzDenialMarkerFromContext(ctx); ok {
		marker.Denied = true
	}
	if !s.config.ScopeChallenge.toolResult(client) {
		w.Header().Set("WWW-Authenticate", challenge)
		writeModernDenied(w, id, "Insufficient scope")
		return true
	}
	meta := map[string]any{"mcp/www_authenticate": []string{challenge}}
	if modern {
		writeModernResult(w, id, newModernCallToolResult(&vmcp.ToolCallResult{
			IsError: true, Meta: meta,
			Content: []vmcp.Content{{Type: vmcp.ContentTypeText, Text: "Additional scopes required"}},
		}, s.config.Name, s.config.Version))
	} else {
		result := mcp.NewToolResultError("Additional scopes required")
		result.Meta = mcp.NewMetaFromMap(meta)
		writeModernResult(w, id, result)
	}
	return true
}

// Discovery describes actual OAuth requirements uniformly, independently of
// refusal encoding, so client profiles do not partition or contaminate tool caches.
func (s *Server) annotateScopeTool(tool *mcp.Tool) {
	if s.config == nil || s.config.ScopeAuthorization == nil || len(s.config.ScopeAuthorization.Tools[tool.Name]) == 0 {
		return
	}
	fields := map[string]any{}
	if tool.Meta != nil {
		maps.Copy(fields, tool.Meta.AdditionalFields)
	}
	fields["securitySchemes"] = []any{map[string]any{
		"type": "oauth2", "scopes": slices.Clone(s.config.ScopeAuthorization.Tools[tool.Name]),
	}}
	meta := mcp.NewMetaFromMap(fields)
	if tool.Meta != nil {
		meta.ProgressToken = tool.Meta.ProgressToken
	}
	tool.Meta = meta
}

func scopeMetadataURL(resource string) (string, error) {
	u, err := url.Parse(resource)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" ||
		u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("scope authorization requires a canonical HTTP(S) gateway resource URL")
	}
	path := u.Path
	if path == "/" {
		path = ""
	}
	u.Path, u.RawPath = oauthproto.WellKnownOAuthResourcePath+path, ""
	return u.String(), nil
}
