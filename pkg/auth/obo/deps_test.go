// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package obo_test

import (
	"os"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPackages_DoNotLinkKubernetes keeps the OBO protocol and middleware
// packages free of client-go, controller-runtime, and the rest of k8s.io /
// sigs.k8s.io. The proxyrunner links pkg/auth/obo through the runner's
// middleware factory map, so a Kubernetes import here would bloat it.
//
// It shells out to `go list -deps`. Without the Go toolchain it skips locally,
// but fails under CI so the guard cannot be bypassed silently.
func TestPackages_DoNotLinkKubernetes(t *testing.T) {
	t.Parallel()

	goBin, err := exec.LookPath("go")
	if err != nil {
		if os.Getenv("CI") != "" {
			t.Fatal("go toolchain required in CI to enforce the k8s-free dependency guard")
		}
		t.Skip("go toolchain not on PATH; skipping dependency-graph assertion")
	}

	const root = "github.com/stacklok/toolhive/pkg/auth/obo"
	packages := []string{
		root,
		root + "/cache",
		root + "/entra",
		root + "/exchange",
	}
	forbidden := []string{"k8s.io/", "sigs.k8s.io/", "client-go", "controller-runtime"}

	for _, pkg := range packages {
		t.Run(pkg, func(t *testing.T) {
			t.Parallel()

			out, err := exec.Command(goBin, "list", "-deps", pkg).CombinedOutput()
			require.NoError(t, err, "go list -deps failed:\n%s", out)

			deps := string(out)
			for _, bad := range forbidden {
				assert.NotContains(t, deps, bad,
					"package %s must not transitively import %q", pkg, bad)
			}
		})
	}
}
