// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package resources provides the management REST API for UI-managed resource
// entries. Resource entries are stored as memory.Entry values with
// source=resource and are read-only to agents via MCP tools.
//
// Routes (all under /api/resources):
//
//	POST   /api/resources         — create resource, embed content, register in MCP
//	GET    /api/resources         — list resources (paginated via ?limit=&offset=)
//	GET    /api/resources/{id}    — get single resource
//	PUT    /api/resources/{id}    — update content (re-embeds), update MCP listing
//	DELETE /api/resources/{id}    — delete resource, unregister from MCP
package resources

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/stacklok/toolhive/pkg/memory"
)

const (
	defaultListLimit = 50
	maxListLimit     = 200
)

// Handler is the management REST API handler for resource entries.
//
// registerFn and unregisterFn are injected by the caller (package main) so
// that the resources package does not import the MCP server package directly.
// They keep the MCP resource listing in sync with the database: registerFn is
// called after a create or update, unregisterFn after a delete.
type Handler struct {
	store        memory.Store
	vectors      memory.VectorStore
	embedder     memory.Embedder
	registerFn   func(memory.Entry)
	unregisterFn func(id string)
	log          *zap.Logger
}

// NewHandler creates a new resource management Handler.
//
// registerFn and unregisterFn are the package-level MCP sync helpers from
// server.go (RegisterResourceEntry / UnregisterResourceEntry), wrapped as
// closures so this package does not need to import the MCP server package.
func NewHandler(
	store memory.Store,
	vectors memory.VectorStore,
	embedder memory.Embedder,
	registerFn func(memory.Entry),
	unregisterFn func(id string),
	log *zap.Logger,
) *Handler {
	return &Handler{
		store:        store,
		vectors:      vectors,
		embedder:     embedder,
		registerFn:   registerFn,
		unregisterFn: unregisterFn,
		log:          log,
	}
}

// ServeHTTP routes /api/resources requests.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Strip /api/resources prefix to get the remaining path.
	path := strings.TrimPrefix(r.URL.Path, "/api/resources")
	path = strings.TrimPrefix(path, "/")

	switch {
	case path == "" && r.Method == http.MethodPost:
		h.create(w, r)
	case path == "" && r.Method == http.MethodGet:
		h.list(w, r)
	case path != "" && r.Method == http.MethodGet:
		h.get(w, r, path)
	case path != "" && r.Method == http.MethodPut:
		h.update(w, r, path)
	case path != "" && r.Method == http.MethodDelete:
		h.delete(w, r, path)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// CreateResourceRequest is the payload for POST /api/resources.
type CreateResourceRequest struct {
	Content string   `json:"content"`
	Type    string   `json:"type"` // semantic | procedural | episodic (default: semantic)
	Tags    []string `json:"tags"`
}

// UpdateResourceRequest is the payload for PUT /api/resources/{id}.
type UpdateResourceRequest struct {
	Content string   `json:"content"`
	Tags    []string `json:"tags"`
}

// ResourceResponse is the API representation of a resource entry.
type ResourceResponse struct {
	ID        string    `json:"id"`
	Content   string    `json:"content"`
	Type      string    `json:"type"`
	Tags      []string  `json:"tags"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func entryToResponse(e memory.Entry) ResourceResponse {
	return ResourceResponse{
		ID:        e.ID,
		Content:   e.Content,
		Type:      string(e.Type),
		Tags:      e.Tags,
		CreatedAt: e.CreatedAt,
		UpdatedAt: e.UpdatedAt,
	}
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	var req CreateResourceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Content) == "" {
		http.Error(w, "content is required", http.StatusBadRequest)
		return
	}

	memType := memory.TypeSemantic
	if req.Type != "" {
		memType = memory.Type(req.Type)
	}

	id := "res_" + uuid.New().String()
	now := time.Now().UTC()
	entry := memory.Entry{
		ID:         id,
		Type:       memType,
		Content:    req.Content,
		Tags:       req.Tags,
		Author:     memory.AuthorHuman,
		Source:     memory.SourceResource,
		Status:     memory.EntryStatusActive,
		TrustScore: 1.0, // resources are always fully trusted
		CreatedAt:  now,
		UpdatedAt:  now,
	}

	if err := h.store.Create(r.Context(), entry); err != nil {
		h.jsonError(w, fmt.Errorf("creating entry: %w", err))
		return
	}

	embedding, err := h.embed(r.Context(), req.Content)
	if err != nil {
		_ = h.store.Delete(r.Context(), id) // rollback
		h.jsonError(w, fmt.Errorf("embedding content: %w", err))
		return
	}
	if err := h.vectors.Upsert(r.Context(), id, embedding); err != nil {
		_ = h.store.Delete(r.Context(), id) // rollback
		h.jsonError(w, fmt.Errorf("storing embedding: %w", err))
		return
	}

	h.registerFn(entry)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(entryToResponse(entry))
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	limit, offset := parsePagination(r)
	src := memory.SourceResource
	entries, err := h.store.List(r.Context(), memory.ListFilter{
		Source: &src,
		Limit:  limit,
		Offset: offset,
	})
	if err != nil {
		h.jsonError(w, err)
		return
	}

	resp := make([]ResourceResponse, 0, len(entries))
	for _, e := range entries {
		resp = append(resp, entryToResponse(e))
	}
	jsonOK(w, resp)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request, id string) {
	entry, err := h.store.Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, memory.ErrNotFound) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		h.jsonError(w, err)
		return
	}
	if entry.Source != memory.SourceResource {
		http.Error(w, "not a resource entry", http.StatusNotFound)
		return
	}
	jsonOK(w, entryToResponse(entry))
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request, id string) {
	entry, err := h.store.Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, memory.ErrNotFound) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		h.jsonError(w, err)
		return
	}
	if entry.Source != memory.SourceResource {
		http.Error(w, "not a resource entry", http.StatusNotFound)
		return
	}

	var req UpdateResourceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Content) == "" {
		http.Error(w, "content is required", http.StatusBadRequest)
		return
	}

	if err := h.store.Update(r.Context(), id, req.Content, memory.AuthorHuman, "updated via management API"); err != nil {
		h.jsonError(w, fmt.Errorf("updating entry: %w", err))
		return
	}

	embedding, err := h.embed(r.Context(), req.Content)
	if err != nil {
		h.log.Warn("failed to re-embed resource after update", zap.String("id", id), zap.Error(err))
	} else if err := h.vectors.Upsert(r.Context(), id, embedding); err != nil {
		h.log.Warn("failed to update embedding after resource update", zap.String("id", id), zap.Error(err))
	}

	updated, err := h.store.Get(r.Context(), id)
	if err != nil {
		h.jsonError(w, err)
		return
	}

	h.registerFn(updated)
	jsonOK(w, entryToResponse(updated))
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request, id string) {
	entry, err := h.store.Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, memory.ErrNotFound) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		h.jsonError(w, err)
		return
	}
	if entry.Source != memory.SourceResource {
		http.Error(w, "not a resource entry", http.StatusNotFound)
		return
	}

	if err := h.store.Delete(r.Context(), id); err != nil {
		h.jsonError(w, fmt.Errorf("deleting entry: %w", err))
		return
	}
	if err := h.vectors.Delete(r.Context(), id); err != nil {
		h.log.Warn("failed to delete embedding for resource", zap.String("id", id), zap.Error(err))
	}

	h.unregisterFn(id)
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) embed(ctx context.Context, text string) ([]float32, error) {
	return h.embedder.Embed(ctx, text)
}

func (h *Handler) jsonError(w http.ResponseWriter, err error) {
	h.log.Warn("resource API error", zap.Error(err))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusInternalServerError)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

func jsonOK(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func parsePagination(r *http.Request) (limit, offset int) {
	limit = defaultListLimit
	if l := r.URL.Query().Get("limit"); l != "" {
		if v, err := strconv.Atoi(l); err == nil && v > 0 {
			limit = min(v, maxListLimit)
		}
	}
	if o := r.URL.Query().Get("offset"); o != "" {
		if v, err := strconv.Atoi(o); err == nil && v >= 0 {
			offset = v
		}
	}
	return limit, offset
}
