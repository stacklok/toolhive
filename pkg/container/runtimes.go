// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package container

// This file imports all container runtime implementations to ensure their init()
// functions are called and they register themselves with the global runtime registry.
//
// When adding a new runtime implementation, add a blank import here.

import (
	// Import Docker runtime to register it
	_ "github.com/stacklok/toolhive/pkg/container/docker"
	// Import Kubernetes runtime to register it
	_ "github.com/stacklok/toolhive/pkg/container/kubernetes"
)
