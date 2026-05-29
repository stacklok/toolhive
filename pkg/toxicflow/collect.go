// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package toxicflow

import (
	"context"
	"fmt"
	"log/slog"

	registrytypes "github.com/stacklok/toolhive-core/registry/types"
	"github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/core"
	"github.com/stacklok/toolhive/pkg/groups"
	"github.com/stacklok/toolhive/pkg/registry"
	"github.com/stacklok/toolhive/pkg/runner"
	"github.com/stacklok/toolhive/pkg/workloads"
)

// Collector gathers the inputs a toxic-flow assessment needs (run configs and
// registry metadata) for the workloads in a group and runs the classifier over
// them. Registry lookups are best-effort: a missing or unreachable registry
// degrades classification accuracy but does not fail the assessment.
type Collector struct {
	workloads workloads.Manager
	registry  registry.Provider // nil when the registry is unavailable
	inference SourceInference
	live      bool
}

// NewCollector builds a Collector backed by the local workloads manager and the
// default registry provider. inference selects the untrusted-content strategy
// (keyword or LLM); a nil inference defaults to KeywordInference. When live is
// true, running (actively-proxied) servers are probed for live tool
// annotations.
func NewCollector(ctx context.Context, inference SourceInference, live bool) (*Collector, error) {
	wm, err := workloads.NewManager(ctx)
	if err != nil {
		return nil, fmt.Errorf("create workloads manager: %w", err)
	}
	// The registry is best-effort: without it, metadata-derived hints are
	// skipped but profile-derived data/sink roles are unaffected.
	reg, err := registry.GetDefaultProvider()
	if err != nil {
		slog.Debug("toxicflow: registry provider unavailable, continuing without metadata", "error", err)
		reg = nil
	}
	if inference == nil {
		inference = NewKeywordInference()
	}
	return &Collector{workloads: wm, registry: reg, inference: inference, live: live}, nil
}

// AssessGroup classifies every workload in the named group and returns the
// group verdict. The overrides apply across the group (each override may target
// a specific server or all servers).
func (c *Collector) AssessGroup(ctx context.Context, group string, overrides []Override) (GroupAssessment, error) {
	all, err := c.workloads.ListWorkloads(ctx, true)
	if err != nil {
		return GroupAssessment{}, fmt.Errorf("list workloads: %w", err)
	}
	members, err := workloads.FilterByGroup(all, group)
	if err != nil {
		return GroupAssessment{}, fmt.Errorf("filter workloads by group %q: %w", group, err)
	}

	assessments := make([]ServerAssessment, 0, len(members))
	for _, w := range members {
		in := ClassifyInput{Name: w.Name, Overrides: overrides}

		if rc, err := runner.LoadState(ctx, w.Name); err == nil {
			in.Config = rc
		} else {
			// Without a run config the profile-derived roles fall back to
			// "unknown"; this is surfaced in the verdict, not an error.
			slog.Debug("toxicflow: could not load run config", "workload", w.Name, "error", err)
		}

		if c.registry != nil {
			if meta, err := c.lookupMetadata(in.Config, w); err == nil {
				in.Metadata = meta
			} else {
				slog.Debug("toxicflow: no registry metadata", "workload", w.Name, "error", err)
			}
		}

		// Untrusted-content (and remote data) hints from the configured strategy.
		if hints, err := c.inference.Infer(ctx, profileFor(w, in.Metadata)); err == nil {
			in.Hints = hints
		} else {
			// Inference is best-effort: on failure the source role stays
			// "unknown" (the safe direction), never a confident "none".
			slog.Debug("toxicflow: inference failed", "workload", w.Name, "error", err)
		}

		// When we are actively proxying this server, probe it live for the
		// authoritative openWorldHint signal.
		if c.live && w.Status == runtime.WorkloadStatusRunning && w.URL != "" {
			if ann, err := probeAnnotations(ctx, w.URL, w.ProxyMode); err == nil {
				in.Annotations = ann
			} else {
				slog.Debug("toxicflow: live probe failed", "workload", w.Name, "error", err)
			}
		}

		assessments = append(assessments, Classify(in))
	}

	return AnalyzeGroup(group, assessments), nil
}

// profileFor builds the public, non-sensitive profile passed to the inference
// strategy. It deliberately carries no permission profile, secrets, or runtime
// config — only catalog metadata an LLM backend may safely receive.
func profileFor(w core.Workload, meta registrytypes.ServerMetadata) ServerProfile {
	p := ServerProfile{Name: w.Name, Remote: w.Remote}
	if meta != nil {
		p.Description = meta.GetDescription()
		p.Overview = meta.GetOverview()
		p.Tags = meta.GetTags()
		p.Tools = meta.GetTools()
	}
	return p
}

// lookupMetadata resolves registry metadata, preferring the registry server
// name recorded in the run config and falling back to the workload name.
func (c *Collector) lookupMetadata(rc *runner.RunConfig, w core.Workload) (registrytypes.ServerMetadata, error) {
	name := w.Name
	if rc != nil && rc.RegistryServerName != "" {
		name = rc.RegistryServerName
	}
	return c.registry.GetServer(name)
}

// ListGroupNames returns the names of all known groups, for auditing every
// group at once.
func ListGroupNames(ctx context.Context) ([]string, error) {
	gm, err := groups.NewManager()
	if err != nil {
		return nil, fmt.Errorf("create groups manager: %w", err)
	}
	gs, err := gm.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list groups: %w", err)
	}
	names := make([]string, 0, len(gs))
	for _, g := range gs {
		names = append(names, g.Name)
	}
	return names, nil
}
