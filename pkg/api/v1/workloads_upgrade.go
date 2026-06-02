// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package v1

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/stacklok/toolhive-core/httperr"
	groupval "github.com/stacklok/toolhive-core/validation/group"
	"github.com/stacklok/toolhive/pkg/groups"
	"github.com/stacklok/toolhive/pkg/registry"
	"github.com/stacklok/toolhive/pkg/runner"
	"github.com/stacklok/toolhive/pkg/state"
	"github.com/stacklok/toolhive/pkg/workloads"
	"github.com/stacklok/toolhive/pkg/workloads/upgrade"
)

// runConfigLoader loads a single workload's saved RunConfig by name.
type runConfigLoader func(ctx context.Context, name string) (*runner.RunConfig, error)

// runConfigLister lists the names of all saved RunConfigs.
type runConfigLister func(ctx context.Context) ([]string, error)

// registryProviderFunc returns a registry provider for upgrade checks.
type registryProviderFunc func() (registry.Provider, error)

// defaultRunConfigLister lists saved RunConfig names from the local state store.
// It mirrors the enumeration used by manager.ListWorkloadsUsingSecret.
func defaultRunConfigLister(ctx context.Context) ([]string, error) {
	store, err := state.NewRunConfigStore(state.DefaultAppName)
	if err != nil {
		return nil, fmt.Errorf("failed to create state store: %w", err)
	}
	return store.List(ctx)
}

// upgradeCheckSingle handles GET /workloads/{name}/upgrade-check.
//
//	@Summary		Check a workload for an available upgrade
//	@Description	Check whether a single workload has a newer image available in
//	@Description	its source registry. This is an offline metadata comparison; it
//	@Description	does not pull images. Secret values are never returned.
//	@Tags			workloads
//	@Produce		json
//	@Param			name	path		string	true	"Workload name"
//	@Success		200		{object}	upgradeCheckResponse
//	@Failure		400		{string}	string	"Bad Request"
//	@Failure		404		{string}	string	"Not Found"
//	@Router			/api/v1beta/workloads/{name}/upgrade-check [get]
func (s *WorkloadRoutes) upgradeCheckSingle(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	name := chi.URLParam(r, "name")

	// Check if workload exists first (mirrors getWorkload's existence check).
	if _, err := s.workloadManager.GetWorkload(ctx, name); err != nil {
		return err // ErrWorkloadNotFound (404) or ErrInvalidWorkloadName (400) already have status codes
	}

	runConfig, err := s.loadRunConfig(ctx, name)
	if err != nil {
		return httperr.WithCode(
			fmt.Errorf("workload configuration not found: %w", err),
			http.StatusNotFound,
		)
	}

	checker, err := s.newUpgradeChecker()
	if err != nil {
		return err
	}

	result, err := checker.Check(ctx, runConfig)
	if err != nil {
		return fmt.Errorf("failed to check workload for upgrade: %w", err)
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(upgradeCheckResponse{Result: result}); err != nil {
		return fmt.Errorf("failed to marshal upgrade check result: %w", err)
	}
	return nil
}

// upgradeCheckBulk handles GET /workloads/upgrade-check.
//
//	@Summary		Check workloads for available upgrades
//	@Description	Check all workloads (optionally filtered by group) for newer
//	@Description	images available in their source registries. This is an offline
//	@Description	metadata comparison; it does not pull images. Secret values are
//	@Description	never returned.
//	@Tags			workloads
//	@Produce		json
//	@Param			all		query		bool	false	"Include stopped workloads"
//	@Param			group	query		string	false	"Filter workloads by group name"
//	@Success		200		{object}	upgradeCheckBulkResponse
//	@Failure		400		{string}	string	"Bad Request"
//	@Failure		404		{string}	string	"Group not found"
//	@Router			/api/v1beta/workloads/upgrade-check [get]
func (s *WorkloadRoutes) upgradeCheckBulk(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	listAll := r.URL.Query().Get("all") == "true"
	groupFilter := r.URL.Query().Get("group")

	// Reuse the exact group/authorization scoping of listWorkloads so the batch
	// endpoint can never report on workloads the caller cannot otherwise list.
	workloadList, err := s.workloadManager.ListWorkloads(ctx, listAll)
	if err != nil {
		return fmt.Errorf("failed to list workloads: %w", err)
	}
	if groupFilter != "" {
		if err := groupval.ValidateName(groupFilter); err != nil {
			return httperr.WithCode(
				fmt.Errorf("invalid group name: %w", err),
				http.StatusBadRequest,
			)
		}
		// FilterByGroup silently returns an empty slice for an unknown group,
		// so check existence explicitly to honor the documented 404.
		exists, existsErr := s.groupManager.Exists(ctx, groupFilter)
		if existsErr != nil {
			return fmt.Errorf("failed to check group existence: %w", existsErr)
		}
		if !exists {
			return fmt.Errorf("%w: %s", groups.ErrGroupNotFound, groupFilter)
		}
		workloadList, err = workloads.FilterByGroup(workloadList, groupFilter)
		if err != nil {
			return err
		}
	}

	// Restrict the set of RunConfigs to those in the scoped workload list.
	inScope := make(map[string]struct{}, len(workloadList))
	for _, wl := range workloadList {
		inScope[wl.Name] = struct{}{}
	}

	configNames, err := s.listRunConfigNames(ctx)
	if err != nil {
		return fmt.Errorf("failed to list workload configurations: %w", err)
	}

	configs := make([]*runner.RunConfig, 0, len(inScope))
	for _, cfgName := range configNames {
		if _, ok := inScope[cfgName]; !ok {
			continue
		}
		runConfig, err := s.loadRunConfig(ctx, cfgName)
		if err != nil {
			// Skip configs we can't load; they may be corrupted or from an older
			// version. The workload simply won't appear in the results.
			slog.Debug("failed to load run config for upgrade check", "workload", cfgName, "error", err)
			continue
		}
		configs = append(configs, runConfig)
	}

	checker, err := s.newUpgradeChecker()
	if err != nil {
		return err
	}

	results := checker.CheckAll(ctx, configs)

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(upgradeCheckBulkResponse{Results: results}); err != nil {
		return fmt.Errorf("failed to marshal upgrade check results: %w", err)
	}
	return nil
}

// newUpgradeChecker builds an upgrade.Checker backed by the registry provider.
func (s *WorkloadRoutes) newUpgradeChecker() (*upgrade.Checker, error) {
	provider, err := s.registryProvider()
	if err != nil {
		return nil, httperr.WithCode(
			fmt.Errorf("failed to get registry provider: %w", err),
			http.StatusServiceUnavailable,
		)
	}
	checker, err := upgrade.NewChecker(provider)
	if err != nil {
		return nil, fmt.Errorf("failed to create upgrade checker: %w", err)
	}
	return checker, nil
}
