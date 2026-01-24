// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

//go:build !linux

package docker

func calculateFinalPort(hostPort int, _ int, _ string) int {
	return hostPort
}
