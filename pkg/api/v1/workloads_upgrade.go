// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package v1

import (
	"context"
	"encoding/json"
	"errors"
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

// workloadUpgradeApplier is the subset of *upgrade.Applier the POST handler
// depends on. It exists so the apply path can be unit tested with a stub,
// without resolving and pulling a real candidate image.
type workloadUpgradeApplier interface {
	Apply(ctx context.Context, name string, opts upgrade.ApplyOptions) (*upgrade.CheckResult, error)
}

// upgradeApplierFactory builds the applier used to materialize an upgrade for a
// single workload. It is always populated in WorkloadRouter with the real
// registry/pull-backed applier; it is injected so tests can supply a stub.
type upgradeApplierFactory func() (workloadUpgradeApplier, error)

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

// upgradeWorkload handles POST /workloads/{name}/upgrade.
//
//	@Summary		Apply an available upgrade to a workload
//	@Description	Apply a registry-sourced upgrade to a single workload. This
//	@Description	re-resolves and verifies the candidate image, pulls it, and only
//	@Description	then recreates the workload with the new image, preserving the
//	@Description	existing configuration. If the workload is already up to date or
//	@Description	is not registry-sourced, the current check result is returned
//	@Description	unchanged (no-op). Secret values are never accepted or returned.
//	@Tags			workloads
//	@Accept			json
//	@Produce		json
//	@Param			name	path		string			true	"Workload name"
//	@Param			request	body		upgradeRequest	false	"Upgrade options"
//	@Success		200		{object}	upgradeCheckResponse
//	@Failure		400		{string}	string	"Bad Request"
//	@Failure		404		{string}	string	"Not Found"
//	@Failure		422		{string}	string	"Unprocessable Entity"
//	@Failure		500		{string}	string	"Internal Server Error"
//	@Router			/api/v1beta/workloads/{name}/upgrade [post]
func (s *WorkloadRoutes) upgradeWorkload(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	name := chi.URLParam(r, "name")

	// Check if workload exists first (mirrors upgradeCheckSingle's existence check).
	if _, err := s.workloadManager.GetWorkload(ctx, name); err != nil {
		return err // ErrWorkloadNotFound (404) or ErrInvalidWorkloadName (400) already have status codes
	}

	// Decode the optional request body. An empty body is valid: it applies the
	// upgrade preserving the workload's existing configuration.
	var req upgradeRequest
	if r.Body != nil && r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			return httperr.WithCode(
				fmt.Errorf("failed to decode request: %w", err),
				http.StatusBadRequest,
			)
		}
	}

	applier, err := s.applierFactory()
	if err != nil {
		return err
	}

	// The API path is always non-interactive: use the detached validator so a
	// candidate that newly requires an unsupplied secret fails loud rather than
	// prompting. s.workloadService.imageVerification keeps API verification in
	// sync with the create/edit paths.
	opts := upgrade.ApplyOptions{
		EnvVars:         req.Env,
		Secrets:         req.Secrets,
		EnvVarValidator: &runner.DetachedEnvVarValidator{},
		VerifySetting:   s.workloadService.imageVerification,
	}

	// Apply is synchronous and returns the CheckResult that drove the upgrade.
	// We deliberately return 200 (not 202) because Apply runs the entire verify
	// -> pull -> recreate sequence inline and reports the applied result; a 202
	// would lose that result and force the client to re-poll. The longTimeout on
	// this route accommodates the image pull.
	result, err := applier.Apply(ctx, name, opts)
	if err != nil {
		// apierrors.ErrorHandler returns the error message verbatim to the client
		// for 4xx codes, so the underlying error must NOT be wrapped into the
		// response: it may reference the request's secret parameters (e.g. an
		// env/secret validation failure). Log the detailed cause server-side and
		// return a sanitized, secret-free message to the caller. The log line
		// carries only the error chain (which itself references secrets by name,
		// never resolved values).
		slog.Error("failed to apply workload upgrade", "workload", name, "error", err)

		// A failure tagged ErrApplyAfterDestroy happened AFTER the destructive
		// recreate began: the workload may be stopped, deleted, or only partially
		// recreated, so its state is uncertain. That is a 5xx-class condition, not
		// a 422 — returning 422 ("request couldn't be processed, nothing changed")
		// would wrongly tell the client it is safe to retry against an intact
		// workload.
		if errors.Is(err, upgrade.ErrApplyAfterDestroy) {
			return httperr.WithCode(
				fmt.Errorf("upgrade of workload %q failed after the recreate began; its state is uncertain", name),
				http.StatusInternalServerError,
			)
		}

		// Preparation failures (resolve/verify/pull/build/validate) map to 422:
		// the request was well-formed but the candidate could not be applied, and
		// the running workload is untouched.
		return httperr.WithCode(
			fmt.Errorf("failed to apply upgrade for workload %q", name),
			http.StatusUnprocessableEntity,
		)
	}

	// On a no-op (already up to date / not registry-sourced) Apply returns the
	// current result without recreating the workload. Either way we return the
	// result so the client can see what happened.
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(upgradeCheckResponse{Result: result}); err != nil {
		return fmt.Errorf("failed to marshal upgrade result: %w", err)
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
