// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

//go:build windows

package discovery

import (
	"context"
	"net"

	"github.com/Microsoft/go-winio"
)

// dialNamedPipe opens a connection to the Windows named pipe at name. The
// caller is expected to have validated name via ParseNamedPipeURL.
func dialNamedPipe(ctx context.Context, name string) (net.Conn, error) {
	return winio.DialPipeContext(ctx, name)
}
