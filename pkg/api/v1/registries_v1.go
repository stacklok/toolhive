// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package v1

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
)

// RegistriesV1Router creates a router for the /v1/registries admin endpoint.
func RegistriesV1Router() http.Handler {
	r := chi.NewRouter()
	r.Get("/", listRegistriesV1)
	r.Get("/{name}", getRegistryV1)
	return r
}

// registryInfoV1 describes a single registry in the list response.
type registryInfoV1 struct {
	Name    string `json:"name"`
	Type    string `json:"type"`    // "local" or "proxied"
	Default bool   `json:"default"` // true if this is the default registry
}

// listRegistriesV1 handles GET /v1/registries
func listRegistriesV1(w http.ResponseWriter, _ *http.Request) {
	store, ok := getRegistryStore(w)
	if !ok {
		return
	}

	names := store.ListRegistries()
	defaultName := store.DefaultRegistryName()

	registries := make([]registryInfoV1, 0, len(names))
	for _, name := range names {
		regType := "local"
		if store.IsProxied(name) {
			regType = "proxied"
		}
		registries = append(registries, registryInfoV1{
			Name:    name,
			Type:    regType,
			Default: name == defaultName,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{
		"registries": registries,
	}); err != nil {
		slog.Error("failed to encode registries response", "error", err)
	}
}

// getRegistryV1 handles GET /v1/registries/{name}
func getRegistryV1(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")

	store, ok := getRegistryStore(w)
	if !ok {
		return
	}

	// Check if the registry exists
	found := false
	for _, n := range store.ListRegistries() {
		if n == name {
			found = true
			break
		}
	}
	if !found {
		writeJSONError(w, http.StatusNotFound, "not_found", "Registry not found")
		return
	}

	regType := "local"
	if store.IsProxied(name) {
		regType = "proxied"
	}

	info := registryInfoV1{
		Name:    name,
		Type:    regType,
		Default: name == store.DefaultRegistryName(),
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(info); err != nil {
		slog.Error("failed to encode registry response", "error", err)
	}
}
