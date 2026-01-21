// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package docker

func calculateFinalPort(hostPort int, firstPortInt int, networkName string) int {
	if networkName == "host" {
		return firstPortInt
	}
	return hostPort
}
