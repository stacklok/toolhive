// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package lifecycle provides the background maintenance job for memory entries.
package lifecycle

import (
	"context"
	"time"

	"go.uber.org/zap"

	"github.com/stacklok/toolhive/pkg/memory"
)

// StalenessAuditThreshold is the score above which entries are logged as audit candidates.
const StalenessAuditThreshold = float32(0.8)

// Job runs periodic maintenance on the memory store: expiring TTL'd entries
// and recomputing trust/staleness scores.
type Job struct {
	store memory.Store
	log   *zap.Logger
}

// New creates a new lifecycle Job.
func New(store memory.Store, log *zap.Logger) *Job {
	return &Job{store: store, log: log}
}

// Run starts the background job, ticking at the given interval until ctx is cancelled.
func (j *Job) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := j.RunOnce(ctx); err != nil {
				j.log.Warn("lifecycle job error", zap.Error(err))
			}
		}
	}
}

// RunOnce executes one maintenance pass: expire TTL'd entries, update scores.
func (j *Job) RunOnce(ctx context.Context) error {
	if err := j.expireEntries(ctx); err != nil {
		return err
	}
	return j.recomputeScores(ctx)
}

func (j *Job) expireEntries(ctx context.Context) error {
	expired, err := j.store.ListExpired(ctx)
	if err != nil {
		return err
	}
	for _, e := range expired {
		if err := j.store.Archive(ctx, e.ID, memory.ArchiveReasonExpired, ""); err != nil {
			j.log.Warn("failed to archive expired entry", zap.String("id", e.ID), zap.Error(err))
		}
	}
	return nil
}

func (j *Job) recomputeScores(ctx context.Context) error {
	entries, err := j.store.ListActive(ctx)
	if err != nil {
		return err
	}
	for _, e := range entries {
		trust := memory.ComputeTrustScore(e)
		staleness := memory.ComputeStalenessScore(e)
		if err := j.store.UpdateScores(ctx, e.ID, trust, staleness); err != nil {
			j.log.Warn("failed to update scores", zap.String("id", e.ID), zap.Error(err))
		}
		if staleness >= StalenessAuditThreshold {
			j.log.Debug("high staleness entry", zap.String("id", e.ID), zap.Float32("staleness", staleness))
		}
	}
	return nil
}
