// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

//go:build windows

package socket

import "net"

// ListenConfig returns a default net.ListenConfig for Windows
// Windows handles socket reuse differently (SO_REUSEADDR allows hijacking),
// so we stick to default behavior for safety.
func ListenConfig() net.ListenConfig {
	return net.ListenConfig{}
}
