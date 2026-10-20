// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package projecttxn serializes mutations to project-scoped ToolHive state.
package projecttxn

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"time"

	"github.com/adrg/xdg"
	"github.com/gofrs/flock"
)

const (
	stripeCount       = 64
	lockRetryInterval = 100 * time.Millisecond
	// Keep the historical skills directory so processes running before and
	// after the transaction was shared still contend on the same lock files.
	lockDirectory = "skills-project-locks"
)

var locks = newLocks()

// Lock acquires the bounded in-process stripe for projectRoot. Waiting is
// governed exclusively by ctx; callers without a deadline may wait until the
// current project mutation completes.
func Lock(ctx context.Context, projectRoot string) (func(), error) {
	token := locks[stripeIndex(projectRoot)]
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-token:
		return func() { token <- struct{}{} }, nil
	}
}

// Run executes fn while holding both the process-wide project stripe and the
// stable OS-backed lock shared by ToolHive processes. Lock files are retained
// after release because deleting one can split waiters across different
// inodes. Skills and AI plugins use this same transaction because both mutate
// toolhive.lock.yaml and related project bookkeeping.
func Run(ctx context.Context, projectRoot string, fn func() error) (err error) {
	unlock, lockErr := Lock(ctx, projectRoot)
	if lockErr != nil {
		return fmt.Errorf("acquiring in-process project transaction lock: %w", lockErr)
	}
	defer unlock()

	lockPath, pathErr := LockPath(projectRoot)
	if pathErr != nil {
		return fmt.Errorf("resolving project transaction lock path: %w", pathErr)
	}
	fileLock := flock.New(lockPath)
	defer func() {
		if closeErr := fileLock.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("releasing project transaction lock: %w", closeErr))
		}
	}()

	locked, fileLockErr := fileLock.TryLockContext(ctx, lockRetryInterval)
	if fileLockErr != nil {
		return fmt.Errorf("acquiring project transaction file lock: %w", fileLockErr)
	}
	if !locked {
		return errors.New("acquiring project transaction file lock: lock was not acquired")
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return fmt.Errorf("starting project transaction: %w", ctxErr)
	}
	return fn()
}

// LockPath returns the stable lock-file path for projectRoot's stripe.
func LockPath(projectRoot string) (string, error) {
	lockDir := filepath.Join(xdg.StateHome, "toolhive", lockDirectory)
	if err := os.MkdirAll(lockDir, 0o700); err != nil {
		return "", fmt.Errorf("creating project transaction lock directory: %w", err)
	}
	return filepath.Join(lockDir, fmt.Sprintf("%02d.lock", stripeIndex(projectRoot))), nil
}

func newLocks() [stripeCount]chan struct{} {
	var result [stripeCount]chan struct{}
	for index := range result {
		result[index] = make(chan struct{}, 1)
		result[index] <- struct{}{}
	}
	return result
}

func stripeIndex(projectRoot string) uint32 {
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(projectRoot))
	return hash.Sum32() % stripeCount
}
