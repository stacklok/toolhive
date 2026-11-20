// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package docker

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeDaemon is a minimal Docker daemon that serves container and network
// lookups for the given names and records every removal request.
type fakeDaemon struct {
	mu         sync.Mutex
	containers map[string]bool // container names that exist
	networks   map[string]bool // network names that exist
	failRemove map[string]bool // container IDs whose removal returns 500
	removed    []string        // "container/<id>" or "network/<id>"
}

func (d *fakeDaemon) handler(t *testing.T) http.HandlerFunc {
	t.Helper()
	// nameFilter extracts the single "name" filter value of a list request.
	nameFilter := func(r *http.Request) string {
		var filters map[string]map[string]bool
		require.NoError(t, json.Unmarshal([]byte(r.URL.Query().Get("filters")), &filters))
		for name := range filters["name"] {
			return name
		}
		return ""
	}
	return func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		defer d.mu.Unlock()
		path := strings.TrimPrefix(r.URL.Path, "/v1.44")
		switch {
		case r.Method == http.MethodGet && path == "/containers/json":
			name := nameFilter(r)
			list := []map[string]any{}
			if d.containers[name] {
				list = append(list, map[string]any{"Id": "id-" + name, "Names": []string{"/" + name}})
			}
			assert.NoError(t, json.NewEncoder(w).Encode(list))
		case r.Method == http.MethodDelete && strings.HasPrefix(path, "/containers/"):
			id := strings.TrimPrefix(path, "/containers/")
			d.removed = append(d.removed, "container/"+id)
			if d.failRemove[id] {
				http.Error(w, `{"message":"cannot remove"}`, http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && path == "/networks":
			name := nameFilter(r)
			list := []map[string]any{}
			if d.networks[name] {
				list = append(list, map[string]any{"Name": name, "Id": "net-" + name})
			}
			assert.NoError(t, json.NewEncoder(w).Encode(list))
		case r.Method == http.MethodDelete && strings.HasPrefix(path, "/networks/"):
			d.removed = append(d.removed, "network/"+strings.TrimPrefix(path, "/networks/"))
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected", http.StatusNotImplemented)
		}
	}
}

func TestCleanupFailedIsolation(t *testing.T) {
	t.Parallel()

	const network = "toolhive-app-internal"
	tests := []struct {
		name        string
		daemon      *fakeDaemon
		wantRemoved []string
	}{
		{
			name: "removes all sidecars and the network",
			daemon: &fakeDaemon{
				containers: map[string]bool{"app-egress": true, "app-ingress": true, "app-dns": true},
				networks:   map[string]bool{network: true},
			},
			wantRemoved: []string{"container/id-app-egress", "container/id-app-ingress", "container/id-app-dns", "network/net-" + network},
		},
		{
			name: "a sidecar that fails to remove does not stop the network deletion",
			daemon: &fakeDaemon{
				containers: map[string]bool{"app-egress": true, "app-ingress": true, "app-dns": true},
				networks:   map[string]bool{network: true},
				failRemove: map[string]bool{"id-app-egress": true},
			},
			wantRemoved: []string{"container/id-app-egress", "container/id-app-ingress", "container/id-app-dns", "network/net-" + network},
		},
		{
			name: "absent network is not an error",
			daemon: &fakeDaemon{
				containers: map[string]bool{"app-dns": true},
				networks:   map[string]bool{},
			},
			wantRemoved: []string{"container/id-app-dns"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			api := discoveryHTTPClient(t, tt.daemon.handler(t))
			client := &Client{client: api, api: api}

			client.cleanupFailedIsolation(t.Context(), "app", network)

			assert.Equal(t, tt.wantRemoved, tt.daemon.removed)
		})
	}
}
