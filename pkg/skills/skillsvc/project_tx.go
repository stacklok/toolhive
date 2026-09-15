// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package skillsvc

import (
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"time"

	"github.com/adrg/xdg"
)

const projectTxLockDir = "skills-project-locks"

const projectTxLockRetryInterval = 100 * time.Millisecond

func newProjectTxLocks() [projectTxStripes]chan struct{} {
	var locks [projectTxStripes]chan struct{}
	for index := range locks {
		locks[index] = make(chan struct{}, 1)
		locks[index] <- struct{}{}
	}
	return locks
}

func projectTxStripeIndex(projectRoot string) uint32 {
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(projectRoot))
	return hash.Sum32() % projectTxStripes
}

// projectTxLockPath places stable transaction locks in ToolHive's state
// directory. The bounded stripe filename gives every process using the same
// ToolHive state a deterministic path without writing lock artifacts into
// the project or following a repository-controlled gitdir path.
func projectTxLockPath(projectRoot string) (string, error) {
	lockDir := filepath.Join(xdg.StateHome, "toolhive", projectTxLockDir)
	if err := os.MkdirAll(lockDir, 0o700); err != nil {
		return "", fmt.Errorf("creating project transaction lock directory: %w", err)
	}
	return filepath.Join(lockDir, fmt.Sprintf("%02d.lock", projectTxStripeIndex(projectRoot))), nil
}
