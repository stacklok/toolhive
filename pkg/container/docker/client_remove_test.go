// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package docker

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	mobyclient "github.com/moby/moby/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/labels"
)

func TestRemoveWorkload_AbsentPrimaryCleansOnlyItsOrphanResources(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name                string
		disappearsOnInspect bool
		collisionLabels     map[string]string
	}{
		{name: "empty lookup preserves unlabeled helper-name collision"},
		{
			name:                "Docker inspect not found preserves nonauxiliary ToolHive collision",
			disappearsOnInspect: true,
			collisionLabels:     map[string]string{"toolhive": "true", "toolhive-basename": "svc-ingress"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var mu sync.Mutex
			remaining := map[string]bool{
				"/containers/helper-egress":     true,
				"/containers/helper-dns":        true,
				"/containers/other-dns":         true,
				"/containers/collision-ingress": true,
				"/networks/internal-svc":        true,
				"/networks/internal-other":      true,
				"/networks/external":            true,
			}
			client := discoveryHTTPClient(t, func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				path := strings.TrimPrefix(r.URL.Path, "/v1.44")
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.Method == http.MethodDelete:
					if !remaining[path] {
						t.Errorf("unexpected or unsafe deletion: %s", path)
						http.Error(w, `{"message":"not found"}`, http.StatusNotFound)
						return
					}
					delete(remaining, path)
					w.WriteHeader(http.StatusNoContent)
				case path == "/containers/json":
					// An unrelated workload must keep the shared external network alive.
					_, _ = w.Write([]byte(`[{"Id":"other","Names":["/other"],"Labels":{"toolhive":"true"}}]`))
				case path == "/networks":
					_, _ = w.Write([]byte(`[{"Id":"internal-svc","Name":"toolhive-svc-internal","Labels":{"toolhive":"true","toolhive-name":"toolhive-svc-internal"}},{"Id":"internal-other","Name":"toolhive-other-internal","Labels":{"toolhive":"true","toolhive-name":"toolhive-other-internal"}},{"Id":"external","Name":"toolhive-external","Labels":{"toolhive":"true","toolhive-name":"toolhive-external"}}]`))
				default:
					t.Errorf("unexpected Docker request: %s %s", r.Method, path)
					http.Error(w, `{"message":"unexpected request"}`, http.StatusInternalServerError)
				}
			})
			candidates := []container.Summary{
				{
					ID: "helper-egress", Names: []string{"/svc-egress"},
					Labels: map[string]string{"toolhive": "true", "toolhive-basename": "svc-egress", ToolhiveAuxiliaryWorkloadLabel: "true"},
				},
				{
					ID: "other-dns", Names: []string{"/mysvc-dns"},
					Labels: map[string]string{"toolhive": "true", "toolhive-basename": "mysvc-dns", ToolhiveAuxiliaryWorkloadLabel: "true"},
				},
				{
					ID: "helper-dns", Names: []string{"/svc-dns"},
					Labels: map[string]string{"toolhive": "true", "toolhive-basename": "svc-dns", ToolhiveAuxiliaryWorkloadLabel: "true"},
				},
				{ID: "collision-ingress", Names: []string{"/svc-ingress"}, Labels: tt.collisionLabels},
			}
			if tt.disappearsOnInspect {
				candidates = append(candidates, container.Summary{
					ID: "missing-primary", Names: []string{"/svc"},
					Labels: map[string]string{"toolhive": "true", "toolhive-basename": "svc"},
				})
			}
			api := &fakeDockerAPI{
				listFunc: func(_ context.Context, opts mobyclient.ContainerListOptions) ([]container.Summary, error) {
					var found []container.Summary
				candidateLoop:
					for _, candidate := range candidates {
						// Honor every requested label so a production ownership filter is exercised.
						for _, filter := range filterValues(opts.Filters, "label") {
							key, value, hasValue := strings.Cut(filter, "=")
							actual, exists := candidate.Labels[key]
							if !exists || (hasValue && actual != value) {
								continue candidateLoop
							}
						}
						names := filterValues(opts.Filters, "name")
						matchesName := len(names) == 0
						for _, name := range names {
							for _, actual := range candidate.Names {
								matchesName = matchesName || strings.Contains(actual, name)
							}
						}
						if matchesName {
							found = append(found, candidate)
						}
					}
					return found, nil
				},
				inspectFunc: func(_ context.Context, id string) (container.InspectResponse, error) {
					assert.True(t, tt.disappearsOnInspect, "empty lookup must not inspect an empty ID")
					assert.Equal(t, "missing-primary", id)
					return container.InspectResponse{}, errdefs.ErrNotFound
				},
				removeFunc: func(_ context.Context, id string, _ mobyclient.ContainerRemoveOptions) error {
					t.Errorf("absent primary must not be removed by empty or stale ID: %q", id)
					return nil
				},
			}
			c := &Client{api: api, client: client}

			require.NoError(t, c.RemoveWorkload(t.Context(), "svc"))
			mu.Lock()
			defer mu.Unlock()
			assert.Equal(t, map[string]bool{
				"/containers/collision-ingress": true,
				"/containers/other-dns":         true,
				"/networks/internal-other":      true,
				"/networks/external":            true,
			}, remaining, "cleanup must remove owned helpers and private network without deleting helper-name collisions or unrelated networks")
		})
	}
}

func TestRemoveWorkload_AbsentPrimaryRequiresNetworkOwnership(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name          string
		networkLabels map[string]string
		wantDeleted   bool
	}{
		{name: "unlabelled network"},
		{name: "missing name label", networkLabels: map[string]string{labels.LabelToolHive: labels.LabelToolHiveValue}},
		{name: "missing ownership label", networkLabels: map[string]string{labels.LabelName: "network"}},
		{name: "false ownership label", networkLabels: map[string]string{labels.LabelToolHive: "false", labels.LabelName: "network"}},
		{name: "empty name label", networkLabels: map[string]string{labels.LabelToolHive: labels.LabelToolHiveValue, labels.LabelName: ""}},
		{name: "owned network", wantDeleted: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var mu sync.Mutex
			var deleted []string
			client := discoveryHTTPClient(t, func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				path := strings.TrimPrefix(r.URL.Path, "/v1.44")
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.Method == http.MethodDelete:
					deleted = append(deleted, path)
					w.WriteHeader(http.StatusNoContent)
				case path == "/containers/json":
					_, _ = w.Write([]byte(`[]`))
				case path == "/networks":
					var networks []network.Summary
					for _, name := range []string{"toolhive-foo-internal", "toolhive-external"} {
						networkLabels := tt.networkLabels
						if tt.wantDeleted {
							networkLabels = make(map[string]string)
							labels.AddNetworkLabels(networkLabels, name)
						}
						networks = append(networks, network.Summary{Network: network.Network{
							ID: name, Name: name, Labels: networkLabels,
						}})
					}
					assert.NoError(t, json.NewEncoder(w).Encode(networks))
				default:
					t.Errorf("unexpected Docker request: %s %s", r.Method, path)
					w.WriteHeader(http.StatusInternalServerError)
				}
			})
			api := &fakeDockerAPI{
				listFunc: func(context.Context, mobyclient.ContainerListOptions) ([]container.Summary, error) {
					return nil, nil
				},
			}
			c := &Client{api: api, client: client}

			require.NoError(t, c.RemoveWorkload(t.Context(), "foo"))
			mu.Lock()
			defer mu.Unlock()
			if tt.wantDeleted {
				assert.Equal(t, []string{"/networks/toolhive-foo-internal", "/networks/toolhive-external"}, deleted)
			} else {
				assert.Empty(t, deleted, "networks without ToolHive ownership labels must survive removal")
			}
		})
	}
}

func TestRemoveWorkload_InspectionFailureDoesNotDeleteResources(t *testing.T) {
	t.Parallel()
	for _, failureAt := range []string{"lookup", "inspect"} {
		t.Run(failureAt, func(t *testing.T) {
			t.Parallel()
			inspectErr := errors.New("Docker daemon unavailable")
			api := &fakeDockerAPI{
				listFunc: func(_ context.Context, _ mobyclient.ContainerListOptions) ([]container.Summary, error) {
					if failureAt == "lookup" {
						return nil, inspectErr
					}
					return []container.Summary{{ID: "primary", Names: []string{"/svc"}}}, nil
				},
				inspectFunc: func(context.Context, string) (container.InspectResponse, error) {
					return container.InspectResponse{}, inspectErr
				},
				removeFunc: func(context.Context, string, mobyclient.ContainerRemoveOptions) error {
					t.Error("failed inspection must not remove a primary container")
					return nil
				},
			}
			client := discoveryHTTPClient(t, func(w http.ResponseWriter, r *http.Request) {
				t.Errorf("failed inspection must not attempt helper or network cleanup: %s %s", r.Method, r.URL.Path)
				w.WriteHeader(http.StatusInternalServerError)
				_ = json.NewEncoder(w).Encode(map[string]string{"message": "unexpected cleanup"})
			})
			c := &Client{api: api, client: client}

			require.ErrorIs(t, c.RemoveWorkload(t.Context(), "svc"), inspectErr)
		})
	}
}
