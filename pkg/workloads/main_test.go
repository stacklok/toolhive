// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package workloads

import (
	"fmt"
	"os"
	"testing"

	"github.com/stacklok/toolhive/pkg/process"
)

const detachedReaperTestEnv = "TOOLHIVE_TEST_WORKLOADS_DETACHED_REAPER_79CBF5"

func TestMain(m *testing.M) {
	mode, fixture := os.LookupEnv(detachedReaperTestEnv)
	if fixture {
		if (mode == "clean" || mode == "error") && os.Getenv(process.ToolHiveDetachedEnv) == process.ToolHiveDetachedValue &&
			len(os.Args) == 4 && os.Args[1] == "start" &&
			os.Args[2] == "detached-reaper-79cbf5-"+mode && os.Args[3] == "--foreground" {
			exitCode := 0
			if mode == "error" {
				exitCode = 42
			}
			fmt.Printf("detached reaper fixture exit %d\n", exitCode)
			os.Exit(exitCode)
		}
		fmt.Fprintf(os.Stderr, "unexpected detached reaper invocation: mode=%q detached=%q args=%q\n",
			mode, os.Getenv(process.ToolHiveDetachedEnv), os.Args)
		os.Exit(2)
	}
	os.Exit(m.Run())
}
