// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package discovery

import (
	"context"
	"fmt"
	"net"
)

// dialNamedPipe is a no-op stub on non-Windows platforms. The npipe:// scheme
// is reachable here only via a misconfigured discovery file or a hand-crafted
// URL; surface a clear error rather than fail with a confusing dial syscall
// result.
func dialNamedPipe(_ context.Context, name string) (net.Conn, error) {
	return nil, fmt.Errorf("named pipes are only supported on Windows: %s", name)
}
