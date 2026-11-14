// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package script

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// parallelCrashChildEnv selects the child-process branch of
// TestParallelListMutationDoesNotCrash. A panic on a parallel() worker
// goroutine terminates the whole process, so the script runs in a re-executed
// test binary and the parent asserts on the child's exit status.
const parallelCrashChildEnv = "THV_SCRIPT_PARALLEL_CRASH_CHILD"

// TestParallelListMutationDoesNotCrash reproduces GHSA-mq57-h84j-5wfp: a
// callable passed to parallel() clears the list parallel() is iterating over,
// so a later worker indexes past the end of the list and panics on a goroutine
// that nothing recovers.
func TestParallelListMutationDoesNotCrash(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		script string
	}{
		{
			// Exactly one child touches the list. The other tasks spin so that
			// most of the 50 tasks are still queued, and so indexed afterwards,
			// when the first child clears the list.
			name: "callable clears the list",
			script: `fns = []
def spin():
    for _ in range(1000):
        pass
def clear():
    fns.clear()
fns.append(clear)
fns.extend([spin] * 49)
return parallel(fns)`,
		},
		{
			name: "callable pops from the list",
			script: `fns = []
def spin():
    for _ in range(1000):
        pass
def shrink():
    for _ in range(45):
        fns.pop()
fns.append(shrink)
fns.extend([spin] * 49)
return parallel(fns)`,
		},
	}

	if script, ok := os.LookupEnv(parallelCrashChildEnv); ok {
		runParallelCrashChild(t, script)
		return
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			t.Cleanup(cancel)

			//nolint:gosec // re-executes the current test binary with a fixed test filter
			cmd := exec.CommandContext(ctx, os.Args[0],
				"-test.run=^TestParallelListMutationDoesNotCrash$", "-test.v")
			cmd.Env = append(os.Environ(), parallelCrashChildEnv+"="+tc.script)
			out, err := cmd.CombinedOutput()
			require.NoError(t, err,
				"script crashed the process running parallel():\n%s", out)
		})
	}
}

// runParallelCrashChild executes script in this process. Reaching the end of
// the function means parallel() returned instead of killing the process; the
// script's own result (success or a script error) does not matter here.
func runParallelCrashChild(t *testing.T, script string) {
	t.Helper()

	_, _ = New(nil, &Config{ParallelMax: 10, StepLimit: 1_000_000}).Execute(context.Background(), script, nil)
}
